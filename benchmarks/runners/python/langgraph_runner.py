#!/usr/bin/env python3
"""LangGraph runner for the cross-framework benchmark.

The graph is the ordinary LangGraph tool-calling shape the framework documents:

    START -> agent --(tool calls?)--> approval --(calls left?)--> tools -> agent -> END

* ``agent`` is a single model call bound to the contract's three tools;
* ``approval`` is the human-in-the-loop node. It raises LangGraph's
  ``interrupt()`` before a ``run_shell`` call, which is what makes the pause a
  durable one: with a checkpointer installed, ``interrupt`` ends the run and
  leaves the thread resumable;
* ``tools`` is ``langgraph.prebuilt.ToolNode``, the framework's own dispatcher;
* durability is ``langgraph.checkpoint.sqlite.SqliteSaver`` writing into
  ``BENCH_STATE_DIR`` -- LangGraph's own saver, not a file format invented here.

Resuming is ``graph.invoke(Command(resume=<decision>), config)`` with the same
``configurable.thread_id``, which is exactly how LangGraph documents resuming an
interrupted thread.
"""

from __future__ import annotations

import sys
from typing import Annotated, Any, TypedDict

from langchain_core.messages import AIMessage, AnyMessage, ToolMessage
from langgraph.checkpoint.sqlite import SqliteSaver
from langgraph.graph import END, START, StateGraph
from langgraph.graph.message import add_messages
from langgraph.prebuilt import ToolNode
from langgraph.types import Command, interrupt

import runner_common as rc

#: A run that keeps asking for approval forever is a runner bug, not a task.
MAX_APPROVALS = 8


class BenchState(TypedDict):
    messages: Annotated[list[AnyMessage], add_messages]


def _framework() -> str:
    from importlib.metadata import version

    return f"langgraph {version('langgraph')}"


def _is_approval_call(message: Any) -> bool:
    return bool(getattr(message, "tool_calls", None))


def build_graph(cfg: rc.Config, model: Any, workspace: rc.Workspace, checkpointer: Any) -> Any:
    tools = rc.build_tools(workspace)
    tool_node = ToolNode(tools)
    # LangGraph's documented tool-calling shape: the model must be bound to the
    # same tools the ToolNode dispatches, or the request advertises no schemas.
    bound_model = model.bind_tools(tools)

    def agent_node(state: BenchState) -> dict[str, Any]:
        return {"messages": [bound_model.invoke(state["messages"])]}

    def approval_node(state: BenchState) -> dict[str, Any]:
        last = state["messages"][-1]
        pending = [call for call in (last.tool_calls or []) if call["name"] == rc.APPROVAL_TOOL]
        if not pending:
            return {}
        request = {
            "action_requests": [
                {
                    "name": call["name"],
                    "args": call["args"],
                    "description": f"Run shell command: {call['args'].get('command', '')}",
                }
                for call in pending
            ],
            "review_configs": [
                {"action_name": call["name"], "allowed_decisions": ["approve", "reject"]}
                for call in pending
            ],
        }
        if cfg.pause_at_approval:
            # Never returns: this process stops here, the SqliteSaver keeps the
            # thread, and the resume process continues from this node.
            interrupt(request)
        decision = cfg.approval
        if decision == "approve":
            return {}
        # The model's own call stays in the assistant message -- the recorded
        # conversation has to show that run_shell was requested -- and the
        # rejection answers it, exactly as LangChain's own HumanInTheLoop
        # middleware does. `answered_calls` below is what keeps an
        # already-answered call out of ToolNode, so the command does not run.
        return {
            "messages": [
                ToolMessage(
                    content=(
                        f"human rejected the {call['name']} call; "
                        f"the command was not executed: {call['args'].get('command', '')}"
                    ),
                    name=call["name"],
                    tool_call_id=call["id"],
                    status="error",
                )
                for call in pending
            ]
        }

    def answered_calls(state: BenchState) -> set[str]:
        return {
            message.tool_call_id
            for message in state["messages"]
            if isinstance(message, ToolMessage) and message.tool_call_id
        }

    def after_agent(state: BenchState) -> str:
        return "approval" if _is_approval_call(state["messages"][-1]) else END

    def after_approval(state: BenchState) -> str:
        answered = answered_calls(state)
        for message in reversed(state["messages"]):
            if isinstance(message, AIMessage):
                unanswered = [
                    call for call in (message.tool_calls or []) if call["id"] not in answered
                ]
                return "tools" if unanswered else "agent"
        return "agent"

    builder = StateGraph(BenchState)
    builder.add_node("agent", agent_node)
    builder.add_node("approval", approval_node)
    builder.add_node("tools", tool_node)
    builder.add_edge(START, "agent")
    builder.add_conditional_edges("agent", after_agent, {"approval": "approval", END: END})
    builder.add_conditional_edges("approval", after_approval, {"tools": "tools", "agent": "agent"})
    builder.add_edge("tools", "agent")
    return builder.compile(checkpointer=checkpointer)


def _run(cfg: rc.Config) -> tuple[str, str]:
    workspace = rc.Workspace(cfg.workspace)
    model = rc.build_model(cfg)
    config = {"configurable": {"thread_id": cfg.thread_id}}

    with SqliteSaver.from_conn_string(str(cfg.checkpoint_db)) as saver:
        graph = build_graph(cfg, model, workspace, saver)
        if cfg.resume_requested:
            snapshot = graph.get_state(config)
            if not snapshot.values or not snapshot.next:
                return (
                    "failed",
                    f"no resumable checkpoint for thread {cfg.thread_id!r} in {cfg.checkpoint_db}",
                )
            # LangGraph's own resume: re-enter the interrupted node with the
            # operator's decision instead of re-running the conversation.
            result = graph.invoke(Command(resume=cfg.approval), config)
        else:
            # The same authored system prompt the deepagents runner sends, so
            # the two Python runners differ only in what their framework adds.
            result = graph.invoke(
                {
                    "messages": [
                        ("system", rc.SYSTEM_PROMPT),
                        ("user", cfg.query),
                    ]
                },
                config,
            )

        approvals = 0
        while True:
            pending = result.get("__interrupt__") if isinstance(result, dict) else None
            if not pending:
                break
            if cfg.pause_at_approval:
                raise rc.RunnerPaused(
                    f"interrupted at the {rc.APPROVAL_TOOL} approval request after "
                    f"{len(result.get('messages', []))} messages; checkpoint kept in "
                    f"{cfg.checkpoint_db}; resume with BENCH_PHASE=resume"
                )
            if approvals >= MAX_APPROVALS:
                return ("failed", f"still asking for approval after {MAX_APPROVALS} decisions")
            approvals += 1
            result = graph.invoke(Command(resume=cfg.approval), config)

    messages = result.get("messages", []) if isinstance(result, dict) else []
    turns = sum(1 for message in messages if isinstance(message, AIMessage))
    return ("completed", f"graph reached END after {turns} model turns and {len(messages)} messages")


def main() -> int:
    return rc.run_runner(_framework(), _run)


if __name__ == "__main__":
    sys.exit(main())
