#!/usr/bin/env python3
"""DeepAgents runner for the cross-framework benchmark.

The task is carried by ``create_deep_agent``'s own public surface:

* ``tools=`` gives the agent the contract's ``read_file``/``write_file``/
  ``run_shell``, and ``middleware=`` swaps deepagents' built-in
  ``FilesystemMiddleware`` for one that advertises no built-in file tool. The
  benchmark pins the tool surface, and deepagents' built-ins
  (``read_file(file_path, offset, limit)``) do not match the frozen
  ``read_file(path)`` scripts and write into a byte-oriented backend rather than
  the workspace, so the runner registers the contract's tools itself. This is
  the replacement hook ``create_deep_agent`` documents: a user middleware whose
  ``name`` matches a base entry replaces it in place.
* ``interrupt_on=`` installs ``HumanInTheLoopMiddleware``: a ``run_shell`` call
  pauses at LangGraph's ``interrupt()`` before the tool runs. The operator's
  answer is delivered as ``Command(resume={"decisions": [{"type": ...}]})``,
  deepagents' own resume contract for HITL.
* ``checkpointer=`` takes LangGraph's ``SqliteSaver`` under ``BENCH_STATE_DIR``,
  so the pause is a durable one and a second, fresh process resumes the same
  ``thread_id``.

Everything the agent loop, prompt and tool dispatch do is deepagents' own; the
runner only builds the agent and answers its approval requests.
"""

from __future__ import annotations

import sys
from typing import Any

from deepagents import HarnessProfile, create_deep_agent
from deepagents.backends.state import StateBackend
from deepagents.middleware.filesystem import FilesystemMiddleware
from deepagents.profiles.harness.harness_profiles import GeneralPurposeSubagentProfile
from deepagents.profiles.harness.harness_profiles import register_harness_profile
from langgraph.checkpoint.sqlite import SqliteSaver
from langgraph.types import Command

import runner_common as rc

#: A run that keeps asking for approval forever is a runner bug, not a task.
MAX_APPROVALS = 8

#: ChatOpenAI reports itself as provider ``openai``, so this is the key
#: deepagents resolves for every model this runner builds.
MODEL_PROVIDER = "openai"

#: deepagents' built-in file tools, which the contract's tool surface replaces.
_BUILTIN_FILESYSTEM_TOOLS = (
    "ls",
    "read_file",
    "write_file",
    "edit_file",
    "delete",
    "glob",
    "grep",
    "execute",
)


def _framework() -> str:
    import deepagents

    return f"deepagents {deepagents.__version__}"


class ContractFilesystemMiddleware(FilesystemMiddleware):
    """``FilesystemMiddleware`` that contributes no built-in file tool.

    The benchmark compares frameworks on a pinned tool set, so the runner
    registers the contract's tools itself. ``FilesystemMiddleware`` refuses an
    empty ``tools=`` list ("read_file must be included"), so the list is emptied
    after construction; ``create_deep_agent`` merges middleware by ``name``, and
    reporting ``FilesystemMiddleware`` here is what makes this instance replace
    the default one instead of being appended next to it.
    """

    def __init__(self, **kwargs: Any) -> None:
        super().__init__(tools=["read_file"], **kwargs)
        self.tools: list[Any] = []

    @property
    def name(self) -> str:
        return "FilesystemMiddleware"


def build_agent(cfg: rc.Config, workspace: rc.Workspace, checkpointer: Any) -> Any:
    # The contract's tool set is three tools; without this the default
    # general-purpose subagent adds the `task` tool and its prompt.
    register_harness_profile(
        MODEL_PROVIDER,
        HarnessProfile(general_purpose_subagent=GeneralPurposeSubagentProfile(enabled=False)),
    )
    return create_deep_agent(
        model=rc.build_model(cfg),
        tools=rc.build_tools(workspace),
        middleware=[ContractFilesystemMiddleware(backend=StateBackend())],
        system_prompt=rc.SYSTEM_PROMPT,
        interrupt_on={rc.APPROVAL_TOOL: {"allowed_decisions": ["approve", "reject"]}},
        checkpointer=checkpointer,
    )


def _decision(cfg: rc.Config) -> dict[str, Any]:
    if cfg.approval == "approve":
        return {"type": "approve"}
    return {"type": "reject", "message": "operator rejected the command"}


def _pending_approvals(result: Any) -> tuple[Any, ...]:
    if not isinstance(result, dict):
        return ()
    return tuple(result.get("__interrupt__") or ())


def _run(cfg: rc.Config) -> tuple[str, str]:
    workspace = rc.Workspace(cfg.workspace)
    config = {"configurable": {"thread_id": cfg.thread_id}}

    with SqliteSaver.from_conn_string(str(cfg.checkpoint_db)) as saver:
        agent = build_agent(cfg, workspace, saver)
        if cfg.resume_requested:
            snapshot = agent.get_state(config)
            if not snapshot.values or not snapshot.next:
                return (
                    "failed",
                    f"no resumable checkpoint for thread {cfg.thread_id!r} in {cfg.checkpoint_db}",
                )
            result = agent.invoke(Command(resume={"decisions": [_decision(cfg)]}), config)
        else:
            result = agent.invoke(
                {"messages": [("user", cfg.query)]},
                config,
            )

        approvals = 0
        while _pending_approvals(result):
            if cfg.pause_at_approval:
                raise rc.RunnerPaused(
                    f"interrupted at the {rc.APPROVAL_TOOL} approval request; checkpoint kept "
                    f"in {cfg.checkpoint_db}; resume with BENCH_PHASE=resume"
                )
            if approvals >= MAX_APPROVALS:
                return ("failed", f"still asking for approval after {MAX_APPROVALS} decisions")
            approvals += 1
            result = agent.invoke(Command(resume={"decisions": [_decision(cfg)]}), config)

    messages = result.get("messages", []) if isinstance(result, dict) else []
    return ("completed", f"agent returned after {len(messages)} messages")


def main() -> int:
    return rc.run_runner(_framework(), _run)


if __name__ == "__main__":
    sys.exit(main())
