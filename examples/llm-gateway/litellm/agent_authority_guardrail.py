"""Agent Authority (or Atryum) as a LiteLLM Proxy guardrail.

LiteLLM relays an agent's model traffic. This guardrail shows Agent Authority
both halves of every exchange:

  before the model is called   -> POST /api/v1/gateway/llm/input
      The tool results in the request are attached to the calls they answer,
      and tools the provider would run itself are allowed or taken out.

  after the model has answered -> POST /api/v1/gateway/llm/output
      Every tool call the model proposed is decided. The calls that may not
      run are replaced, in the response the agent receives, by a sentence
      saying why. The agent never sees them, so it cannot run them.

If Agent Authority cannot be reached, the request is refused: nothing passes
unchecked.

Streaming. Tool calls arrive in fragments on a stream and cannot be judged in
pieces, so the guardrail collects them. The model's words still stream to the
agent as they arrive. From the first tool call on, the rest of the stream is
held back until the response is complete; Agent Authority then decides every
call, and the agent receives the remainder: the calls it may run, and a
sentence in place of each one it may not. This works for Chat Completions
(/v1/chat/completions) and Anthropic Messages (/v1/messages). A streamed
Responses request (/v1/responses) that carries tools is still refused.

`on_stream` changes that: "check" (the default) does the above, "refuse"
refuses every streamed request that carries tools, and "allow" lets streamed
responses through UNCHECKED.

Register it in LiteLLM's config.yaml (see config.yaml beside this file):

    guardrails:
      - guardrail_name: agent-authority
        litellm_params:
          guardrail: agent_authority_guardrail.AgentAuthorityGuardrail
          mode: [pre_call, post_call]
          default_on: true
          api_base: https://aa.example.com
          api_key: os.environ/AGENT_AUTHORITY_GATEWAY_KEY

The key is an agent key marked as a gateway's (the "asserter" capability).
"""

import json
from typing import Any, AsyncIterator, Optional

import httpx
from fastapi import HTTPException
from litellm.integrations.custom_guardrail import CustomGuardrail
from litellm.types.utils import ModelResponseStream

# LiteLLM's call types -> Agent Authority's name for the wire format.
FORMATS = {
    "completion": "openai_chat",
    "acompletion": "openai_chat",
    "anthropic_messages": "anthropic",
    "aanthropic_messages": "anthropic",
    "responses": "openai_responses",
    "aresponses": "openai_responses",
}

# The parts of a request Agent Authority reads. LiteLLM's `data` also holds
# its own bookkeeping (keys, metadata, the raw HTTP request); none of that is
# sent.
REQUEST_FIELDS = (
    "model", "messages", "input", "system", "tools", "functions", "tool_choice",
    "mcp_servers", "web_search_options", "stream",
)
# The parts Agent Authority may change: the tools on offer.
ADJUSTABLE_FIELDS = ("tools", "tool_choice", "mcp_servers", "web_search_options")
# The formats whose streams this guardrail can collect.
COLLECTED_STREAMS = ("openai_chat", "anthropic")


class AgentAuthorityGuardrail(CustomGuardrail):
    def __init__(self, api_base: Optional[str] = None, api_key: Optional[str] = None,
                 on_stream: str = "check", **kwargs: Any):
        if not api_base or not api_key:
            raise ValueError("agent-authority guardrail: api_base and api_key are required")
        if on_stream not in ("check", "refuse", "allow"):
            raise ValueError('agent-authority guardrail: on_stream must be "check", "refuse" or "allow"')
        self.api_base = api_base.rstrip("/")
        self.on_stream = on_stream
        self.http = httpx.AsyncClient(timeout=8.0, headers={"Authorization": f"Bearer {api_key}"})
        super().__init__(**kwargs)

    # ---- before the model is called -------------------------------------

    async def async_pre_call_hook(self, user_api_key_dict, cache, data: dict, call_type):
        fmt = FORMATS.get(call_type)
        if fmt is None:
            if _declares_tools(data):
                # A call type this guardrail cannot read (Gemini's
                # generateContent, realtime, websockets, batches): its tool
                # calls would reach the agent unchecked.
                _refuse(400, f"Agent Authority cannot check the tool calls of a {call_type} request at this "
                             "gateway. Use /v1/chat/completions, /v1/messages or /v1/responses.")
            return data  # embeddings, images, ...: no tools to govern
        if data.get("stream") and _declares_tools(data) and self.on_stream != "allow":
            if self.on_stream == "refuse" or fmt not in COLLECTED_STREAMS:
                _refuse(400, "Agent Authority cannot check the tool calls of a streamed response at this "
                             'gateway. Send the request without "stream": true.')
        request = {k: data[k] for k in REQUEST_FIELDS if k in data}
        answer = await self._ask("llm/input", {"format": fmt, "request": request, **_who(user_api_key_dict, data)})
        adjusted = answer.get("request")
        if adjusted is None:
            _refuse(403, answer.get("reason") or "Agent Authority refused this request.")
        if answer.get("changed"):
            for field in ADJUSTABLE_FIELDS:
                if field in adjusted:
                    data[field] = adjusted[field]
                else:
                    data.pop(field, None)
        return data

    # ---- after the model has answered -------------------------------------

    async def async_post_call_success_hook(self, data: dict, user_api_key_dict, response):
        fmt = _format_of(response)
        if fmt is None:
            if _declares_tools(data):
                # A response this guardrail cannot read may carry tool calls.
                _refuse(403, f"Agent Authority cannot check the tool calls in a {type(response).__name__} "
                             "response at this gateway; it was withheld.")
            return response
        body = response if isinstance(response, dict) else response.model_dump()
        request = {k: data[k] for k in REQUEST_FIELDS if k in data}
        answer = await self._ask("llm/output", {"format": fmt, "response": body, "request": request,
                                                **_who(user_api_key_dict, data)})
        adjusted = answer.get("response")
        if adjusted is None:
            _refuse(403, answer.get("reason") or "Agent Authority withheld this response.")
        if not answer.get("changed"):
            return response
        if isinstance(response, dict):  # Anthropic Messages: a plain dict
            response.clear()
            response.update(adjusted)
            return response
        # LiteLLM's own response object: rebuild it from the adjusted body, and
        # update the original in place too, for versions that ignore the return.
        rebuilt = type(response)(**adjusted)
        for field in ("choices", "output"):
            if hasattr(rebuilt, field) and hasattr(response, field):
                setattr(response, field, getattr(rebuilt, field))
        return response

    # ---- a streamed answer ---------------------------------------------------

    async def async_post_call_streaming_iterator_hook(self, user_api_key_dict, response: AsyncIterator,
                                                      request_data: dict):
        """Passes the model's words through as they arrive, holds everything
        from the first tool call on, and sends the rest once Agent Authority
        has decided every call in the finished response."""
        if not _declares_tools(request_data) or self.on_stream == "allow":
            async for item in response:
                yield item
            return
        collector = None
        async for item in response:
            if collector is None:
                collector = _collector_for(item)
            for passed in collector.feed(item):
                yield passed
        if collector is None:
            return
        if collector.failed:  # the provider's stream ended in an error
            if collector.proposed_calls:
                _refuse(502, "The model's stream failed part-way through a tool call; nothing was sent on.")
            for held in collector.finish(None):
                yield held
            return
        request = {k: request_data[k] for k in REQUEST_FIELDS if k in request_data}
        answer = await self._ask("llm/output", {"format": collector.format, "response": collector.response(),
                                                "request": request, **_who(user_api_key_dict, request_data)})
        adjusted = answer.get("response")
        if adjusted is None:
            _refuse(403, answer.get("reason") or "Agent Authority withheld this response.")
        for rest in collector.finish(adjusted if answer.get("changed") else None):
            yield rest

    async def _ask(self, hook: str, payload: dict) -> dict:
        try:
            reply = await self.http.post(f"{self.api_base}/api/v1/gateway/{hook}", json=payload)
        except httpx.HTTPError as err:
            _refuse(503, f"Agent Authority unavailable: {err}")
        if reply.status_code != 200:
            _refuse(503, f"Agent Authority unavailable: {hook} answered {reply.status_code}")
        return reply.json()


def _collector_for(item: Any):
    if isinstance(item, ModelResponseStream):
        return _ChatStream()
    if isinstance(item, (bytes, str)):
        return _MessagesStream()
    _refuse(400, f"Agent Authority cannot read a {type(item).__name__} stream at this gateway. "
                 'Send the request without "stream": true.')


def _get(obj: Any, key: str) -> Any:
    if obj is None:
        return None
    if isinstance(obj, dict):
        return obj.get(key)
    return getattr(obj, key, None)


class _ChatStream:
    """Collects a Chat Completions stream (LiteLLM's ModelResponseStream chunks).

    Chunks that carry only words are passed on at once. From the first chunk
    that carries a tool call or a finish reason, chunks are held.
    """

    format = "openai_chat"

    def __init__(self) -> None:
        self.first: Any = None
        self.live = True
        self.sent: dict = {}       # choice index -> the text already sent
        self.role_sent: set = set()
        self.held: list = []
        self.choices: dict = {}
        self.usage: Any = None
        self.failed = False

    @property
    def proposed_calls(self) -> bool:
        return any(c["tool_calls"] or c["function_call"] for c in self.choices.values())

    def feed(self, chunk: Any) -> list:
        if self.first is None:
            self.first = chunk
        choices = _get(chunk, "choices") or []
        for choice in choices:
            self._absorb(choice)
        if _get(chunk, "usage"):
            self.usage = _get(chunk, "usage")
        if not choices:
            self.held.append(chunk)  # usage alone: sent at the end
            return []
        if self.live and all(self._only_words(c) for c in choices):
            for c in choices:
                index = _get(c, "index") or 0
                delta = _get(c, "delta")
                self.sent[index] = self.sent.get(index, "") + (_get(delta, "content") or "")
                if _get(delta, "role"):
                    self.role_sent.add(index)
            return [chunk]
        self.live = False
        self.held.append(chunk)
        return []

    @staticmethod
    def _only_words(choice: Any) -> bool:
        delta = _get(choice, "delta")
        return (_get(choice, "finish_reason") is None and not _get(delta, "tool_calls")
                and not _get(delta, "function_call") and isinstance(_get(delta, "content") or "", str))

    def _absorb(self, choice: Any) -> None:
        index = _get(choice, "index") or 0
        c = self.choices.setdefault(index, {"role": None, "content": None, "tool_calls": {},
                                            "function_call": None, "finish_reason": None})
        delta = _get(choice, "delta")
        if _get(delta, "role"):
            c["role"] = _get(delta, "role")
        if _get(delta, "content"):
            c["content"] = (c["content"] or "") + _get(delta, "content")
        for position, tc in enumerate(_get(delta, "tool_calls") or []):
            i = _get(tc, "index")
            slot = c["tool_calls"].setdefault(position if i is None else i,
                                              {"id": None, "type": "function", "function": {"name": "", "arguments": ""}})
            if _get(tc, "id"):
                slot["id"] = _get(tc, "id")
            fn = _get(tc, "function")
            if _get(fn, "name") and not slot["function"]["name"]:
                slot["function"]["name"] = _get(fn, "name")
            slot["function"]["arguments"] += _get(fn, "arguments") or ""
        fc = _get(delta, "function_call")
        if fc:
            c["function_call"] = c["function_call"] or {"name": "", "arguments": ""}
            if _get(fc, "name") and not c["function_call"]["name"]:
                c["function_call"]["name"] = _get(fc, "name")
            c["function_call"]["arguments"] += _get(fc, "arguments") or ""
        if _get(choice, "finish_reason"):
            c["finish_reason"] = _get(choice, "finish_reason")

    def response(self) -> dict:
        """The finished response, as a non-streamed call would have returned it."""
        choices = []
        for index in sorted(self.choices):
            c = self.choices[index]
            message: dict = {"role": c["role"] or "assistant", "content": c["content"]}
            if c["tool_calls"]:
                message["tool_calls"] = [c["tool_calls"][i] for i in sorted(c["tool_calls"])]
            if c["function_call"]:
                message["function_call"] = c["function_call"]
            choices.append({"index": index, "message": message, "finish_reason": c["finish_reason"]})
        body = {"id": _get(self.first, "id"), "object": "chat.completion", "created": _get(self.first, "created"),
                "model": _get(self.first, "model"), "choices": choices}
        if self.usage:
            body["usage"] = self.usage if isinstance(self.usage, dict) else self.usage.model_dump()
        return body

    def finish(self, adjusted: Optional[dict]) -> list:
        """What is left to send: the held chunks as they were, or the rest of
        the adjusted response written as chunks."""
        if adjusted is None:
            return self.held
        out = []
        for choice in adjusted.get("choices") or []:
            index = choice.get("index") or 0
            message = choice.get("message") or {}
            content, sent = message.get("content"), self.sent.get(index, "")
            if content is not None and not isinstance(content, str):
                _refuse(502, "Agent Authority's answer cannot be streamed: its text is not a string.")
            if sent and not (content or "").startswith(sent):
                _refuse(502, "Agent Authority changed text that had already been streamed; nothing more was sent.")
            delta: dict = {}
            if index not in self.role_sent:
                delta["role"] = message.get("role") or "assistant"
            if content and content[len(sent):]:
                delta["content"] = content[len(sent):]
            if delta:
                out.append(self._chunk([{"index": index, "delta": delta, "finish_reason": None}]))
            calls = message.get("tool_calls") or []
            if calls:
                out.append(self._chunk([{"index": index, "finish_reason": None, "delta": {"tool_calls": [
                    {"index": i, "id": tc.get("id"), "type": tc.get("type") or "function", "function": tc.get("function")}
                    for i, tc in enumerate(calls)]}}]))
            if message.get("function_call"):
                out.append(self._chunk([{"index": index, "finish_reason": None,
                                         "delta": {"function_call": message["function_call"]}}]))
            out.append(self._chunk([{"index": index, "delta": {}, "finish_reason": choice.get("finish_reason")}]))
        if self.usage:  # last, as LiteLLM sends it when the agent asked for usage
            out.append(self._chunk([{"index": 0, "delta": {}}], usage=self.usage))
        return out

    def _chunk(self, choices: list, **extra: Any) -> ModelResponseStream:
        return ModelResponseStream(id=_get(self.first, "id"), created=_get(self.first, "created"),
                                   model=_get(self.first, "model"), object="chat.completion.chunk", choices=choices,
                                   **extra)


class _MessagesStream:
    """Collects an Anthropic Messages stream, which LiteLLM relays as raw
    server-sent-event bytes.

    Events are passed on whole and at once until a content block that is not
    text or thinking starts (a tool call). From then on events are held.
    """

    format = "anthropic"
    WORDS = ("text", "thinking", "redacted_thinking")

    def __init__(self) -> None:
        self.pending = ""
        self.live = True
        self.passed_blocks = 0   # blocks sent whole before holding began
        self.held: list = []     # raw events, each ending in a blank line
        self.message: dict = {}
        self.partial_json: dict = {}
        self.failed = False
        self.proposed_calls = False
        self.as_bytes = True

    def feed(self, item: Any) -> list:
        if isinstance(item, str):
            self.as_bytes = False
            text = item
        else:
            text = item.decode("utf-8")
        self.pending += text.replace("\r\n", "\n")
        passed = []
        while "\n\n" in self.pending:
            raw, self.pending = self.pending.split("\n\n", 1)
            if raw.strip():
                passed.extend(self._event(raw + "\n\n"))
        if not passed:
            return []
        joined = "".join(passed)
        return [joined.encode("utf-8") if self.as_bytes else joined]

    def _event(self, raw: str) -> list:
        data = {}
        for line in raw.splitlines():
            if line.startswith("data:"):
                try:
                    data = json.loads(line[5:].strip())
                except ValueError:
                    data = {}
        kind = data.get("type")
        if kind == "message_start":
            self.message = dict(data.get("message") or {})
            self.message["content"] = []
        elif kind == "content_block_start":
            index, block = data.get("index", 0), dict(data.get("content_block") or {})
            content = self.message.setdefault("content", [])
            while len(content) <= index:
                content.append(None)
            content[index] = block
            if block.get("type") not in self.WORDS:
                self.proposed_calls = True
                if self.live:
                    self.live, self.passed_blocks = False, index
        elif kind == "content_block_delta":
            index, delta = data.get("index", 0), data.get("delta") or {}
            block = (self.message.get("content") or [None] * (index + 1))[index] or {}
            dtype = delta.get("type")
            if dtype == "text_delta":
                block["text"] = block.get("text", "") + delta.get("text", "")
            elif dtype == "input_json_delta":
                self.partial_json[index] = self.partial_json.get(index, "") + delta.get("partial_json", "")
            elif dtype == "thinking_delta":
                block["thinking"] = block.get("thinking", "") + delta.get("thinking", "")
            elif dtype == "signature_delta":
                block["signature"] = delta.get("signature")
            elif dtype == "citations_delta":
                block.setdefault("citations", []).append(delta.get("citation"))
        elif kind == "content_block_stop":
            index = data.get("index", 0)
            if index in self.partial_json:
                text = self.partial_json.pop(index)
                try:
                    self.message["content"][index]["input"] = json.loads(text) if text.strip() else {}
                except (ValueError, IndexError, TypeError):
                    _refuse(502, "The model's stream had a tool call whose arguments could not be read.")
        elif kind == "message_delta":
            for k, v in (data.get("delta") or {}).items():
                self.message[k] = v
            if data.get("usage"):
                self.message["usage"] = {**(self.message.get("usage") or {}), **data["usage"]}
        elif kind == "error":
            self.failed = True
        # message_delta and message_stop carry the stop reason, which may change.
        if self.live and kind not in ("message_delta", "message_stop", "error"):
            return [raw]
        self.held.append(raw)
        return []

    def response(self) -> dict:
        return self.message

    def finish(self, adjusted: Optional[dict]) -> list:
        if self.pending.strip():
            self._event(self.pending + "\n\n")
            self.pending = ""
        if adjusted is None:
            out = "".join(self.held)
        else:
            content = adjusted.get("content") or []
            if content[: self.passed_blocks] != (self.message.get("content") or [])[: self.passed_blocks]:
                _refuse(502, "Agent Authority changed text that had already been streamed; nothing more was sent.")
            events = []
            for index in range(self.passed_blocks if not self.live else len(self.message.get("content") or []),
                               len(content)):
                events.extend(_block_events(index, content[index]))
            events.append(("message_delta", {"type": "message_delta",
                                             "delta": {"stop_reason": adjusted.get("stop_reason"),
                                                       "stop_sequence": adjusted.get("stop_sequence")},
                                             "usage": adjusted.get("usage") or {}}))
            events.append(("message_stop", {"type": "message_stop"}))
            out = "".join(f"event: {name}\ndata: {json.dumps(body)}\n\n" for name, body in events)
        return [out.encode("utf-8") if self.as_bytes else out] if out else []


def _block_events(index: int, block: dict) -> list:
    """One content block as the events a provider would stream for it."""
    kind = block.get("type")
    deltas = []
    if kind == "text":
        opening = {"type": "text", "text": ""}
        deltas.append({"type": "text_delta", "text": block.get("text", "")})
    elif kind == "thinking":
        opening = {"type": "thinking", "thinking": ""}
        deltas.append({"type": "thinking_delta", "thinking": block.get("thinking", "")})
        if block.get("signature"):
            deltas.append({"type": "signature_delta", "signature": block["signature"]})
    elif "input" in block:  # tool_use, server_tool_use, mcp_tool_use
        opening = {**block, "input": {}}
        deltas.append({"type": "input_json_delta", "partial_json": json.dumps(block["input"])})
    else:  # results and anything else arrive whole
        opening = block
    events = [("content_block_start", {"type": "content_block_start", "index": index, "content_block": opening})]
    events += [("content_block_delta", {"type": "content_block_delta", "index": index, "delta": d}) for d in deltas]
    events.append(("content_block_stop", {"type": "content_block_stop", "index": index}))
    return events


def _declares_tools(data: dict) -> bool:
    config = data.get("config") if isinstance(data.get("config"), dict) else {}
    return bool(data.get("tools") or data.get("functions") or data.get("mcp_servers")
                or data.get("tool_config") or data.get("toolConfig") or config.get("tools"))


def _format_of(response: Any) -> Optional[str]:
    """Which wire format a LiteLLM response is in, or None if it has no tools."""
    if isinstance(response, dict):
        return "anthropic" if response.get("type") == "message" else None
    name = type(response).__name__
    if name == "ModelResponse":
        return "openai_chat"
    if name == "ResponsesAPIResponse":
        return "openai_responses"
    return None


def _who(user_api_key_dict: Any, data: dict) -> dict:
    """Who the call is for, as LiteLLM verified it from the caller's key.

    Never read from the request body: an agent could write any name there.
    """
    out: dict = {}
    # user_id only: LiteLLM fills end_user_id from the request ("user",
    # metadata.user_id, customer headers) without checking it by default, so
    # a caller could name anyone there.
    user = getattr(user_api_key_dict, "user_id", None)
    if user:
        out["user"] = {"id": str(user), "type": "user"}
    alias = getattr(user_api_key_dict, "key_alias", None) or getattr(user_api_key_dict, "team_alias", None)
    if alias:
        out["agent"] = {"id": str(alias)}
    metadata = data.get("metadata") or data.get("litellm_metadata") or {}
    session = data.get("litellm_session_id") or (metadata.get("session_id") if isinstance(metadata, dict) else None)
    if session:
        out["session_id"] = str(session)
    return out


def _refuse(status: int, reason: str) -> None:
    raise HTTPException(status_code=status, detail={"error": reason})
