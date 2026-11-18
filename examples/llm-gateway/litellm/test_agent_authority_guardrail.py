"""Tests for the guardrail's handling of streamed answers.

They need LiteLLM installed, so run them where the proxy runs, for example
in the LiteLLM image with this folder mounted at /cfg:

    docker exec -e PYTHONPATH=/cfg -e PYTHONDONTWRITEBYTECODE=1 <container> \
        python -m unittest discover -s /cfg -p 'test_*.py' -v

Agent Authority is replaced by a function that records what it was asked and
returns a fixed answer, except where a test needs it to be unreachable.
"""

import json
import unittest

from fastapi import HTTPException
from litellm.types.utils import ModelResponseStream

from agent_authority_guardrail import AgentAuthorityGuardrail

TOOLS = [{"type": "function", "function": {"name": n, "parameters": {"type": "object"}}}
         for n in ("get_ticket", "refund_payment", "export_customer_data")]
NOTICE = "[Agent Authority] The call to export_customer_data was blocked and did not run."


def guardrail(api_base="http://aa.test"):
    return AgentAuthorityGuardrail(api_base=api_base, api_key="atr_test", guardrail_name="agent-authority")


def fake_aa(g, answer):
    """Stands in for Agent Authority: records every question, gives `answer`."""
    asked = []

    async def ask(hook, payload):
        asked.append((hook, payload))
        return answer(payload) if callable(answer) else answer

    g._ask = ask
    return asked


async def stream_of(items):
    for item in items:
        yield item


async def run(g, items, request):
    """Drives the hook, noting how many items had gone out when AA was asked."""
    out, seen_at_ask = [], []
    original = g._ask

    async def ask(hook, payload):
        seen_at_ask.append(len(out))
        return await original(hook, payload)

    g._ask = ask
    async for item in g.async_post_call_streaming_iterator_hook(None, stream_of(items), request):
        out.append(item)
    return out, (seen_at_ask[0] if seen_at_ask else None)


# ---- Chat Completions --------------------------------------------------------

def chat_chunk(delta, finish=None, **extra):
    return ModelResponseStream(id="chatcmpl-1", created=1, model="mock-chat", object="chat.completion.chunk",
                               choices=[{"index": 0, "delta": delta, "finish_reason": finish}], **extra)


def chat_stream():
    """A provider's stream: words, then two tool calls whose arguments arrive in fragments."""
    chunks = [chat_chunk({"role": "assistant", "content": ""}),
              chat_chunk({"content": "I'll look it up "}),
              chat_chunk({"content": "and send the export."})]
    for i, (call_id, name, args) in enumerate([("call_a", "get_ticket", '{"ticket_id":7}'),
                                              ("call_b", "export_customer_data", '{"scope":"all"}')]):
        chunks.append(chat_chunk({"tool_calls": [{"index": i, "id": call_id, "type": "function",
                                                  "function": {"name": name, "arguments": ""}}]}))
        for piece in (args[:5], args[5:]):
            chunks.append(chat_chunk({"tool_calls": [{"index": i, "function": {"arguments": piece}}]}))
    chunks.append(chat_chunk({}, finish="tool_calls"))
    chunks.append(chat_chunk({}, usage={"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}))
    return chunks


def withhold_export(payload):
    """What Agent Authority does to the chat response: export taken out, a notice added."""
    body = json.loads(json.dumps(payload["response"]))
    msg = body["choices"][0]["message"]
    msg["tool_calls"] = [tc for tc in msg["tool_calls"] if tc["function"]["name"] != "export_customer_data"]
    msg["content"] = msg["content"] + "\n\n" + NOTICE
    return {"response": body, "changed": True}


def chat_text(chunks):
    return "".join(c.choices[0].delta.content or "" for c in chunks if c.choices)


def chat_calls(chunks):
    return [(tc.id, tc.function.name, tc.function.arguments)
            for c in chunks if c.choices for tc in (c.choices[0].delta.tool_calls or [])]


class ChatStreamTest(unittest.IsolatedAsyncioTestCase):
    async def test_words_stream_at_once_and_only_allowed_calls_reach_the_agent(self):
        g = guardrail()
        asked = fake_aa(g, withhold_export)
        out, sent_before_ask = await run(g, chat_stream(), {"model": "mock-chat", "tools": TOOLS, "stream": True})

        self.assertEqual(sent_before_ask, 3, "the three chunks of words go out before Agent Authority is asked")
        hook, payload = asked[0]
        self.assertEqual(hook, "llm/output")
        self.assertEqual(payload["format"], "openai_chat")
        sent_calls = payload["response"]["choices"][0]["message"]["tool_calls"]
        self.assertEqual([(c["id"], c["function"]["arguments"]) for c in sent_calls],
                         [("call_a", '{"ticket_id":7}'), ("call_b", '{"scope":"all"}')],
                         "the fragments are put back together before Agent Authority sees them")

        self.assertEqual(chat_text(out), "I'll look it up and send the export.\n\n" + NOTICE)
        self.assertEqual(chat_calls(out), [("call_a", "get_ticket", '{"ticket_id":7}')])
        self.assertEqual(out[-2].choices[0].finish_reason, "tool_calls")
        self.assertEqual(out[-1].usage.total_tokens, 15, "the usage still comes last")

    async def test_an_unchanged_answer_is_sent_as_the_provider_streamed_it(self):
        g = guardrail()
        fake_aa(g, lambda p: {"response": p["response"], "changed": False})
        chunks = chat_stream()
        out, _ = await run(g, chunks, {"tools": TOOLS, "stream": True})
        self.assertEqual([id(c) for c in out], [id(c) for c in chunks])

    async def test_every_call_withheld_ends_the_turn(self):
        g = guardrail()

        def withhold_all(payload):
            body = json.loads(json.dumps(payload["response"]))
            msg = body["choices"][0]["message"]
            del msg["tool_calls"]
            msg["content"] += "\n\n" + NOTICE
            body["choices"][0]["finish_reason"] = "stop"
            return {"response": body, "changed": True}

        fake_aa(g, withhold_all)
        out, _ = await run(g, chat_stream(), {"tools": TOOLS, "stream": True})
        self.assertEqual(chat_calls(out), [])
        self.assertEqual(out[-2].choices[0].finish_reason, "stop")

    async def test_no_tool_call_reaches_the_agent_when_agent_authority_is_unreachable(self):
        g = guardrail(api_base="http://127.0.0.1:9")  # nothing listens there
        out = []
        with self.assertRaises(HTTPException) as refused:
            async for item in g.async_post_call_streaming_iterator_hook(
                    None, stream_of(chat_stream()), {"tools": TOOLS, "stream": True}):
                out.append(item)
        self.assertEqual(refused.exception.status_code, 503)
        self.assertEqual(chat_calls(out), [])

    async def test_a_request_without_tools_is_passed_through_unasked(self):
        g = guardrail()
        asked = fake_aa(g, {"response": None})
        chunks = [chat_chunk({"content": "hello"}), chat_chunk({}, finish="stop")]
        out, _ = await run(g, chunks, {"stream": True})
        self.assertEqual(out, chunks)
        self.assertEqual(asked, [])


# ---- Anthropic Messages --------------------------------------------------------

def sse(event, data):
    return f"event: {event}\ndata: {json.dumps(data)}\n\n"


def messages_stream():
    events = [sse("message_start", {"type": "message_start", "message": {
                  "id": "msg_1", "type": "message", "role": "assistant", "model": "mock-claude", "content": [],
                  "stop_reason": None, "stop_sequence": None, "usage": {"input_tokens": 10, "output_tokens": 1}}}),
              sse("ping", {"type": "ping"}),
              sse("content_block_start", {"type": "content_block_start", "index": 0,
                                          "content_block": {"type": "text", "text": ""}}),
              sse("content_block_delta", {"type": "content_block_delta", "index": 0,
                                          "delta": {"type": "text_delta", "text": "Looking it up."}}),
              sse("content_block_stop", {"type": "content_block_stop", "index": 0})]
    for i, (call_id, name, args) in enumerate([("toolu_a", "get_ticket", '{"ticket_id": 7}'),
                                              ("toolu_b", "export_customer_data", '{"scope": "all"}')], start=1):
        events.append(sse("content_block_start", {"type": "content_block_start", "index": i, "content_block": {
            "type": "tool_use", "id": call_id, "name": name, "input": {}}}))
        for piece in (args[:6], args[6:]):
            events.append(sse("content_block_delta", {"type": "content_block_delta", "index": i,
                                                      "delta": {"type": "input_json_delta", "partial_json": piece}}))
        events.append(sse("content_block_stop", {"type": "content_block_stop", "index": i}))
    events.append(sse("message_delta", {"type": "message_delta", "delta": {"stop_reason": "tool_use"},
                                        "usage": {"output_tokens": 30}}))
    events.append(sse("message_stop", {"type": "message_stop"}))
    raw = "".join(events).encode()
    return [raw[i:i + 37] for i in range(0, len(raw), 37)]  # cut anywhere, as a network does


def read_events(items):
    text = b"".join(items).decode()
    return [json.loads(line[5:]) for line in text.splitlines() if line.startswith("data:")]


class MessagesStreamTest(unittest.IsolatedAsyncioTestCase):
    async def test_text_streams_at_once_and_a_withheld_call_becomes_a_notice(self):
        g = guardrail()

        def withhold(payload):
            body = json.loads(json.dumps(payload["response"]))
            body["content"][2] = {"type": "text", "text": NOTICE}
            return {"response": body, "changed": True}

        asked = fake_aa(g, withhold)
        out, sent_before_ask = await run(g, messages_stream(), {"tools": TOOLS, "stream": True})

        before = read_events(out[:sent_before_ask])
        self.assertEqual([e["type"] for e in before],
                         ["message_start", "ping", "content_block_start", "content_block_delta", "content_block_stop"],
                         "the text block goes out whole before Agent Authority is asked")
        response = asked[0][1]["response"]
        self.assertEqual(asked[0][1]["format"], "anthropic")
        self.assertEqual([b.get("input") for b in response["content"]], [None, {"ticket_id": 7}, {"scope": "all"}])
        self.assertEqual(response["stop_reason"], "tool_use")

        after = read_events(out[sent_before_ask:])
        starts = [e["content_block"] for e in after if e["type"] == "content_block_start"]
        self.assertEqual([(b["type"], b.get("name")) for b in starts], [("tool_use", "get_ticket"), ("text", None)])
        self.assertIn(NOTICE, [e["delta"].get("text") for e in after if e["type"] == "content_block_delta"])
        self.assertEqual([e["type"] for e in after][-2:], ["message_delta", "message_stop"])
        self.assertTrue(all(isinstance(item, bytes) for item in out))

    async def test_an_unchanged_answer_is_sent_byte_for_byte(self):
        g = guardrail()
        fake_aa(g, lambda p: {"response": p["response"], "changed": False})
        items = messages_stream()
        out, _ = await run(g, items, {"tools": TOOLS, "stream": True})
        self.assertEqual(b"".join(out), b"".join(items))

    async def test_a_withheld_response_is_refused(self):
        g = guardrail()
        fake_aa(g, {"reason": "denied"})
        with self.assertRaises(HTTPException) as refused:
            await run(g, messages_stream(), {"tools": TOOLS, "stream": True})
        self.assertEqual(refused.exception.status_code, 403)

    async def test_a_stream_that_fails_inside_a_tool_call_is_refused(self):
        g = guardrail()
        fake_aa(g, {"response": None})
        events = b"".join(messages_stream()).decode().split("\n\n")
        cut = next(i for i, e in enumerate(events) if '"input_json_delta"' in e)
        broken = "\n\n".join(events[:cut + 1]) + "\n\n" + sse("error", {"type": "error", "error": {"type": "overloaded_error"}})
        with self.assertRaises(HTTPException) as refused:
            await run(g, [broken.encode()], {"tools": TOOLS, "stream": True})
        self.assertEqual(refused.exception.status_code, 502)


# ---- Before the model is called ------------------------------------------------

class StreamedRequestTest(unittest.IsolatedAsyncioTestCase):
    async def test_streams_it_cannot_collect_are_refused_before_the_model_is_called(self):
        for on_stream, call_type, refused in [("check", "acompletion", False), ("check", "aanthropic_messages", False),
                                              ("check", "aresponses", True), ("refuse", "acompletion", True),
                                              ("allow", "aresponses", False)]:
            g = AgentAuthorityGuardrail(api_base="http://aa.test", api_key="atr_test",
                                        guardrail_name="agent-authority", on_stream=on_stream)
            fake_aa(g, lambda p: {"request": p["request"], "changed": False})
            data = {"model": "m", "tools": TOOLS, "stream": True, "messages": []}
            with self.subTest(on_stream=on_stream, call_type=call_type):
                if refused:
                    with self.assertRaises(HTTPException) as e:
                        await g.async_pre_call_hook(None, None, data, call_type)
                    self.assertEqual(e.exception.status_code, 400)
                else:
                    self.assertIs(await g.async_pre_call_hook(None, None, data, call_type), data)


if __name__ == "__main__":
    unittest.main()
