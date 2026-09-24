"""Shared protocol layer for the Python cross-framework benchmark runners.

Everything in this module implements the frozen contract in
``benchmarks/README.md``: the environment variables, the three tools
(``read_file``/``write_file``/``run_shell``) confined to ``BENCH_WORKSPACE``,
the ``BENCH_APPROVAL`` decision, the ``BENCH_RESULT`` JSON shape and the four
exit codes (0 completed, 75 paused, 78 unsupported, 1 failed).

Both runners talk to the endpoint with LangChain's canonical OpenAI client,
``langchain_openai.ChatOpenAI``, so the integration under test is the
framework's own supported path rather than an adapter written for this
benchmark. The one configuration the scripted endpoint forces is
``use_responses_api=False``: ``langchain-openai`` 1.x defaults OpenAI models to
the Responses API, and the frozen endpoint serves chat completions.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import traceback
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable

from langchain_core.tools import BaseTool, tool
from langchain_openai import ChatOpenAI

# ---------------------------------------------------------------------------
# Contract constants
# ---------------------------------------------------------------------------

TASKS = ("edit-file", "approve-command", "durable-task")
PHASES = ("run", "resume")
APPROVALS = ("approve", "reject")

#: The tool whose call is an approval request. The contract's task table names
#: "a command that needs approval" for ``approve-command`` and "a tool that
#: requires approval" for ``durable-task``; ``run_shell`` is the only tool in
#: the frozen set that can carry that meaning.
APPROVAL_TOOL = "run_shell"

STATUS_EXIT = {"completed": 0, "paused": 75, "unsupported": 78, "failed": 1}

DEFAULT_SHELL_TIMEOUT = 60.0
MAX_READ_BYTES = 1 << 20  # 1 MiB: enough for a benchmark workspace, bounded memory

SYSTEM_PROMPT = (
    "You are a tool-using agent inside a benchmark harness. "
    "Use the provided tools to complete the task. Act instead of explaining."
)


def task_prompt(task: str) -> str:
    """Fallback user message, used only when ``BENCH_QUERY`` is absent.

    The frozen task instruction normally arrives in ``BENCH_QUERY``; inventing
    one here would make the prompt-bytes column meaningless, so this path warns
    on stderr when it runs."""
    return f"Complete the benchmark task '{task}' using the available tools."


# ---------------------------------------------------------------------------
# Control-flow signals
# ---------------------------------------------------------------------------


class ConfigError(Exception):
    """The environment does not satisfy the runner protocol."""


class RunnerPaused(Exception):
    """The runner stopped durably with work left (exit 75)."""

    def __init__(self, detail: str) -> None:
        super().__init__(detail)
        self.detail = detail


class RunnerUnsupported(Exception):
    """The framework cannot do what the task requires (exit 78)."""

    def __init__(self, detail: str) -> None:
        super().__init__(detail)
        self.detail = detail


class ToolError(Exception):
    """A tool call was refused; the message is returned to the model."""


# ---------------------------------------------------------------------------
# Environment
# ---------------------------------------------------------------------------

REQUIRED_ENV = (
    "BENCH_BASE_URL",
    "BENCH_API_KEY",
    "BENCH_MODEL",
    "BENCH_TASK",
    "BENCH_WORKSPACE",
    "BENCH_STATE_DIR",
    "BENCH_PHASE",
    "BENCH_APPROVAL",
    "BENCH_RESULT",
)


@dataclass(frozen=True)
class Config:
    base_url: str
    api_key: str
    model: str
    task: str
    query: str
    workspace: Path
    state_dir: Path
    phase: str
    approval: str
    require_pause: bool
    result_path: Path

    @classmethod
    def from_env(cls, environ: dict[str, str] | None = None) -> "Config":
        env = os.environ if environ is None else environ
        missing = [name for name in REQUIRED_ENV if not env.get(name)]
        if missing:
            raise ConfigError(f"missing required environment: {', '.join(sorted(missing))}")

        task = env["BENCH_TASK"]
        if task not in TASKS:
            raise ConfigError(f"BENCH_TASK must be one of {list(TASKS)}, got {task!r}")
        phase = env["BENCH_PHASE"]
        if phase not in PHASES:
            raise ConfigError(f"BENCH_PHASE must be one of {list(PHASES)}, got {phase!r}")
        approval = env["BENCH_APPROVAL"]
        if approval not in APPROVALS:
            raise ConfigError(f"BENCH_APPROVAL must be one of {list(APPROVALS)}, got {approval!r}")

        # The frozen task instruction, so the prompt cost the report attributes
        # to a framework is the harness's text and not something a runner made
        # up. The fallback exists only because the variable is newer than the
        # rest of the contract; when it fires, the runner says so on stderr.
        query = env.get("BENCH_QUERY", "").strip()
        if not query:
            query = task_prompt(task)
            print(
                f"[bench] BENCH_QUERY is not set; using the generated instruction for {task}",
                file=sys.stderr,
            )

        workspace = Path(env["BENCH_WORKSPACE"])
        if not workspace.is_absolute():
            raise ConfigError(f"BENCH_WORKSPACE must be absolute, got {workspace}")
        if not workspace.is_dir():
            raise ConfigError(f"BENCH_WORKSPACE does not exist: {workspace}")

        state_dir = Path(env["BENCH_STATE_DIR"])
        if not state_dir.is_absolute():
            raise ConfigError(f"BENCH_STATE_DIR must be absolute, got {state_dir}")
        state_dir.mkdir(parents=True, exist_ok=True)

        base_url = env["BENCH_BASE_URL"]
        if not base_url.startswith(("http://", "https://")):
            raise ConfigError(f"BENCH_BASE_URL must be an http(s) URL, got {base_url!r}")

        result_path = Path(env["BENCH_RESULT"])
        if not result_path.is_absolute():
            raise ConfigError(f"BENCH_RESULT must be absolute, got {result_path}")

        return cls(
            base_url=base_url,
            api_key=env["BENCH_API_KEY"],
            model=env["BENCH_MODEL"],
            task=task,
            query=query,
            workspace=workspace.resolve(),
            state_dir=state_dir.resolve(),
            phase=phase,
            approval=approval,
            require_pause=env.get("BENCH_REQUIRE_PAUSE", "0") not in ("", "0", "false", "False"),
            result_path=result_path,
        )

    @property
    def pause_at_approval(self) -> bool:
        """True when this process must stop durably instead of answering the
        approval request. The harness sets ``BENCH_REQUIRE_PAUSE=1`` for the
        first process of ``durable-task``; a resume process never pauses."""
        return self.require_pause and self.phase == "run"

    @property
    def resume_requested(self) -> bool:
        return self.phase == "resume"

    @property
    def checkpoint_db(self) -> Path:
        """Where the framework's own durable saver keeps state."""
        return self.state_dir / "checkpoints.sqlite"

    @property
    def thread_id(self) -> str:
        """Stable across the two processes of ``durable-task``: the pause and
        the resume must name the same LangGraph thread."""
        return f"bench-{self.task}"


# ---------------------------------------------------------------------------
# Workspace + tools
# ---------------------------------------------------------------------------


class Workspace:
    """The three contracted tools, confined to ``BENCH_WORKSPACE``."""

    def __init__(self, root: Path) -> None:
        self.root = root.resolve()

    def resolve(self, path: str) -> Path:
        if not isinstance(path, str) or not path.strip():
            raise ToolError("path must be a non-empty string")
        candidate = Path(path)
        full = candidate.resolve() if candidate.is_absolute() else (self.root / candidate).resolve()
        if full != self.root and not full.is_relative_to(self.root):
            raise ToolError(f"path escapes the workspace: {path!r}")
        return full

    def relative(self, full: Path) -> str:
        return "." if full == self.root else str(full.relative_to(self.root))

    def read_file(self, path: str) -> str:
        full = self.resolve(path)
        if not full.is_file():
            raise ToolError(f"no such file: {self.relative(full)}")
        data = full.read_bytes()[:MAX_READ_BYTES]
        text = data.decode("utf-8", errors="replace")
        return text

    def write_file(self, path: str, content: str) -> str:
        full = self.resolve(path)
        if not isinstance(content, str):
            content = "" if content is None else str(content)
        full.parent.mkdir(parents=True, exist_ok=True)
        full.write_text(content, encoding="utf-8")
        return f"wrote {len(content.encode('utf-8'))} bytes to {self.relative(full)}"

    def run_shell(self, command: str) -> str:
        if not isinstance(command, str) or not command.strip():
            raise ToolError("command must be a non-empty string")
        try:
            proc = subprocess.run(
                ["/bin/sh", "-c", command],
                cwd=str(self.root),
                capture_output=True,
                text=True,
                timeout=DEFAULT_SHELL_TIMEOUT,
            )
        except subprocess.TimeoutExpired:
            raise ToolError(f"command timed out after {DEFAULT_SHELL_TIMEOUT:.0f}s: {command}") from None
        parts = [f"exit code: {proc.returncode}"]
        if proc.stdout:
            parts.append(f"stdout:\n{proc.stdout.rstrip()}")
        if proc.stderr:
            parts.append(f"stderr:\n{proc.stderr.rstrip()}")
        return "\n".join(parts)


def build_tools(workspace: Workspace) -> list[BaseTool]:
    """The three contracted tools as LangChain tools with the exact names and
    argument names the turn scripts use."""

    def _guarded(fn: Callable[..., str]) -> Callable[..., str]:
        def wrapper(*args: Any, **kwargs: Any) -> str:
            try:
                return fn(*args, **kwargs)
            except ToolError as exc:
                return f"ERROR: {exc}"
            except OSError as exc:
                return f"ERROR: {type(exc).__name__}: {exc}"

        return wrapper

    @tool("read_file")
    def read_file(path: str) -> str:
        """Read a UTF-8 text file from the workspace and return its contents."""
        return _guarded(workspace.read_file)(path)

    @tool("write_file")
    def write_file(path: str, content: str) -> str:
        """Write a text file in the workspace, creating parent directories."""
        return _guarded(workspace.write_file)(path, content)

    @tool("run_shell")
    def run_shell(command: str) -> str:
        """Run a shell command with the workspace as its working directory."""
        return _guarded(workspace.run_shell)(command)

    return [read_file, write_file, run_shell]


# ---------------------------------------------------------------------------
# Result writing
# ---------------------------------------------------------------------------


def _one_line(text: str, limit: int = 400) -> str:
    line = " ".join(str(text).split())
    if len(line) > limit:
        line = line[: limit - 3] + "..."
    return line


def write_result(
    result_path: Path,
    *,
    task: str,
    phase: str,
    status: str,
    detail: str,
    framework: str,
) -> None:
    payload = {
        "task": task,
        "phase": phase,
        "status": status,
        "detail": _one_line(detail),
        "framework": framework,
    }
    result_path.parent.mkdir(parents=True, exist_ok=True)
    tmp = result_path.with_name(result_path.name + ".tmp")
    tmp.write_text(json.dumps(payload, ensure_ascii=False) + "\n", encoding="utf-8")
    os.replace(tmp, result_path)


def run_runner(framework: str, main: Callable[[Config], tuple[str, str]]) -> int:
    """Run ``main`` and always write ``BENCH_RESULT``, then return the exit
    code the protocol maps the status to. ``main`` returns ``(status, detail)``
    or raises :class:`RunnerPaused` / :class:`RunnerUnsupported`."""
    result_path = os.environ.get("BENCH_RESULT")
    task = os.environ.get("BENCH_TASK", "")
    phase = os.environ.get("BENCH_PHASE", "")

    try:
        cfg = Config.from_env()
    except BaseException as exc:  # noqa: BLE001 - the protocol demands a result
        detail = _one_line(f"{type(exc).__name__}: {exc}")
        print(f"[{framework}] environment error: {detail}", file=sys.stderr)
        if result_path:
            write_result(
                Path(result_path),
                task=task,
                phase=phase,
                status="failed",
                detail=detail,
                framework=framework,
            )
        return 1

    task, phase = cfg.task, cfg.phase
    status, detail = "failed", "runner reported no outcome"
    try:
        status, detail = main(cfg)
    except RunnerPaused as paused:
        status, detail = "paused", paused.detail
    except RunnerUnsupported as unsupported:
        status, detail = "unsupported", unsupported.detail
    except BaseException as exc:  # noqa: BLE001 - the protocol demands a result
        traceback.print_exc()
        status, detail = "failed", _one_line(f"{type(exc).__name__}: {exc}")

    if status not in STATUS_EXIT:
        detail = f"runner returned invalid status {status!r}: {detail}"
        status = "failed"

    code = STATUS_EXIT[status]
    write_result(
        cfg.result_path,
        task=task,
        phase=phase,
        status=status,
        detail=detail,
        framework=framework,
    )
    print(f"[{framework}] {task}/{phase} -> {status} (exit {code}): {_one_line(detail)}", file=sys.stderr)
    return code


# ---------------------------------------------------------------------------
# The model every Python runner talks to the endpoint with
# ---------------------------------------------------------------------------


def build_model(cfg: Config) -> ChatOpenAI:
    """LangChain's canonical OpenAI client, pointed at the scripted endpoint.

    ``use_responses_api=False`` is not an optimization: ``langchain-openai``
    1.x defaults OpenAI models to the Responses API, and the frozen endpoint
    (``internal/scripted``) serves ``/v1/chat/completions`` only. Everything
    else -- retries, payload fields, tool schemas, message conversion -- is
    ChatOpenAI's own default behavior, so the recorded prompt bytes are the
    framework's, not this runner's.
    """
    return ChatOpenAI(
        model=cfg.model,
        base_url=cfg.base_url,
        api_key=cfg.api_key,
        use_responses_api=False,
    )


__all__ = [
    "APPROVAL_TOOL",
    "APPROVALS",
    "Config",
    "ConfigError",
    "PHASES",
    "RunnerPaused",
    "RunnerUnsupported",
    "STATUS_EXIT",
    "SYSTEM_PROMPT",
    "TASKS",
    "ToolError",
    "Workspace",
    "build_model",
    "build_tools",
    "run_runner",
    "task_prompt",
    "write_result",
]
