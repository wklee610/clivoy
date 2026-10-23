#!/usr/bin/env python3
"""Exercise a built Clivoy binary with official SDKs and a local fake upstream.

No cloud credentials, CLI login, GPU, model download, or paid request is used.
Usage: python scripts/sdk_smoke.py --binary ./clivoy
"""

from __future__ import annotations

import argparse
import base64
import json
import os
from pathlib import Path
import socket
from socketserver import TCPServer
import subprocess
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import anthropic
import openai


TEXT = "Hello from Clivoy 🌍."
MODEL = "test-model"
VISION_MODEL = "vision-model"
IMAGE_MODEL = "image-model"
KNOWLEDGE_MODEL = "knowledge-model"
FALLBACK_MODEL = "fallback-model"
KNOWLEDGE_QUERY = "What is the backup procedure?"
KNOWLEDGE_USER = {"role": "user", "content": KNOWLEDGE_QUERY}
KNOWLEDGE_TITLE = "Backup handbook"
KNOWLEDGE_URL = "https://example.invalid/handbook/backup"
KNOWLEDGE_TEXT = "Backup procedure: run the synthetic backup job every night."
# A 1x1 PNG fixture; no image model is invoked by these tests.
PNG_BASE64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
PNG_BYTES = base64.b64decode(PNG_BASE64)
IMAGE_DATA_URI = "data:image/png;base64," + PNG_BASE64
REMOTE_IMAGE = "https://example.invalid/clivoy-sdk-image.png"
TOKEN = "sdk-smoke-local-token"
UPSTREAM_TOKEN = "sdk-smoke-upstream-token"
SCHEMA = {
    "type": "object",
    "properties": {"city": {"type": "string"}},
    "required": ["city"],
    "additionalProperties": False,
}
FUNCTION = {"name": "look_up", "description": "Look up a city.", "parameters": SCHEMA}
CHAT_TOOL = {"type": "function", "function": FUNCTION}
RESPONSE_TOOL = {"type": "function", **FUNCTION}
ANTHROPIC_TOOL = {"name": "look_up", "description": "Look up a city.", "input_schema": SCHEMA}
USER = {"role": "user", "content": "Hello"}


class FakeUpstream(BaseHTTPRequestHandler):
    """A deliberately small OpenAI Chat Completions server with fragmented SSE."""

    protocol_version = "HTTP/1.1"

    def log_message(self, *_args):
        pass

    def reply(self, status, value):
        body = json.dumps(value).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.close_connection = True
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        self.server.paths.append(self.path)
        if self.path not in ("/v1/chat/completions", "/v1/images/generations"):
            self.reply(404, {"error": {"message": "Unexpected upstream path"}})
            return
        if self.headers.get("Authorization") != f"Bearer {UPSTREAM_TOKEN}":
            self.reply(401, {"error": {"message": "Missing upstream token"}})
            return
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.server.requests.append(body)
        if self.path == "/v1/images/generations":
            self.generate_image(body)
            return
        if body.get("model") != "upstream-model":
            self.reply(400, {"error": {"message": "Model alias was not resolved"}})
            return
        if "[upstream-failure]" in json.dumps(body):
            self.reply(503, {"error": {"message": "Intentional test failure"}})
            return
        tool_result = any(m["role"] == "tool" for m in body["messages"])
        tool_call = bool(body.get("tools")) and not tool_result
        limited = body.get("max_tokens", body.get("max_completion_tokens")) == 1
        text = "Hello" if limited else TEXT
        finish = "tool_calls" if tool_call else "length" if limited else "stop"
        usage = {"prompt_tokens": 7, "completion_tokens": 1 if limited else 4,
                 "total_tokens": 8 if limited else 11}
        call = {"id": "call_test", "type": "function",
                "function": {"name": "look_up", "arguments": '{"city":"Seoul"}'}}
        common = {"id": "chatcmpl-test", "created": 1720000000, "model": "upstream-model"}
        if not body.get("stream"):
            message = {"role": "assistant", "content": None if tool_call else text}
            if tool_call:
                message["tool_calls"] = [call]
            self.reply(200, {**common, "object": "chat.completion", "usage": usage,
                             "choices": [{"index": 0, "message": message, "finish_reason": finish}]})
            return
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Connection", "close")
        self.end_headers()
        self.close_connection = True

        def chunk(delta=None, reason=None, final_usage=False):
            payload = {**common, "object": "chat.completion.chunk", "choices": []}
            if final_usage:
                payload["usage"] = usage
            else:
                payload["choices"] = [{"index": 0, "delta": delta or {}, "finish_reason": reason}]
            self.wfile.write(("data: " + json.dumps(payload, ensure_ascii=False) + "\n\n").encode())
            self.wfile.flush()

        chunk({"role": "assistant", "content": ""})
        if tool_call:
            chunk({"tool_calls": [{"index": 0, "id": "call_test", "type": "function",
                                    "function": {"name": "look_", "arguments": ""}}]})
            chunk({"tool_calls": [{"index": 0, "function": {"name": "up", "arguments": ""}}]})
            for fragment in ['{"city":', '"Se', 'oul"}']:
                chunk({"tool_calls": [{"index": 0, "function": {"arguments": fragment}}]})
        else:
            for fragment in [text[:5], text[5:12], text[12:]]:
                if fragment:
                    chunk({"content": fragment})
        chunk(reason=finish)
        if body.get("stream_options", {}).get("include_usage"):
            chunk(final_usage=True)
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()

    def generate_image(self, body):
        if body.get("model") != "upstream-image":
            self.reply(400, {"error": {"message": "Image model alias was not resolved"}})
            return
        if "[upstream-failure]" in body["prompt"]:
            self.reply(503, {"error": {"message": "Intentional image upstream failure"}})
            return
        if "[malformed-image]" in body["prompt"]:
            self.reply(200, {"created": 1720000000, "data": [{"b64_json": "invalid!"}]})
            return
        image = {"revised_prompt": "A tiny test fixture."}
        if body.get("response_format") == "url":
            image["url"] = REMOTE_IMAGE
        else:
            image["b64_json"] = PNG_BASE64
        self.reply(200, {
            "created": 1720000000,
            "data": [image for _ in range(body.get("n", 1))],
            "output_format": "png", "size": "1024x1024", "quality": "low", "background": "opaque",
            "usage": {"input_tokens": 9, "output_tokens": 11, "total_tokens": 20,
                      "input_tokens_details": {"text_tokens": 9, "image_tokens": 0}},
        })


class LocalTestServer(ThreadingHTTPServer):
    def server_bind(self):
        # HTTPServer normally performs a reverse-DNS lookup, unnecessary for a
        # loopback fixture and potentially slow on offline developer machines.
        TCPServer.server_bind(self)
        self.server_name = "localhost"
        self.server_port = self.server_address[1]


def openai_client(base, token=TOKEN):
    return openai.OpenAI(base_url=base + "/v1", api_key=token, max_retries=0, timeout=10,
                         http_client=openai.DefaultHttpxClient(trust_env=False),
                         _strict_response_validation=True)


def anthropic_client(base, token=TOKEN):
    return anthropic.Anthropic(base_url=base, api_key=token, max_retries=0, timeout=10,
                               http_client=anthropic.DefaultHttpxClient(trust_env=False),
                               _strict_response_validation=True)


class SDKCompatibility(unittest.TestCase):
    base_url = ""
    upstream = None

    @classmethod
    def setUpClass(cls):
        cls.oai = openai_client(cls.base_url)
        cls.claude = anthropic_client(cls.base_url)

    @classmethod
    def tearDownClass(cls):
        cls.oai.close()
        cls.claude.close()

    def test_models(self):
        models = self.oai.models.list()
        self.assertEqual([(m.id, m.owned_by) for m in models.data],
                         [(FALLBACK_MODEL, "test"), (IMAGE_MODEL, "test"), (KNOWLEDGE_MODEL, "test"),
                          (MODEL, "test"), (VISION_MODEL, "test")])
        self.assertEqual({m.id: m.model_dump()["capabilities"] for m in models.data}, {
            MODEL: ["chat"], VISION_MODEL: ["chat", "vision"], IMAGE_MODEL: ["image_generation"],
            KNOWLEDGE_MODEL: ["chat"],
            FALLBACK_MODEL: ["chat"],
        })

    def test_chat(self):
        result = self.oai.chat.completions.create(model=MODEL, messages=[USER])
        self.assertEqual(result.model, MODEL)
        self.assertEqual(result.choices[0].message.content, TEXT)
        self.assertEqual(result.choices[0].finish_reason, "stop")
        self.assertEqual(result.usage.total_tokens, 11)

    def assert_knowledge_answer(self, text):
        footer = f"\n\nSources:\n- [1] [{KNOWLEDGE_TITLE}]({KNOWLEDGE_URL})"
        self.assertEqual(text, TEXT + footer)
        payload = self.upstream.requests[-1]
        self.assertEqual(payload["model"], "upstream-model")
        self.assertEqual(payload["messages"][-1], KNOWLEDGE_USER)
        context = payload["messages"][0]
        self.assertEqual(context["role"], "system")
        self.assertIn(KNOWLEDGE_TEXT, context["content"])
        self.assertIn(KNOWLEDGE_URL, context["content"])
        self.assertIn("backup.md", context["content"])
        self.assertNotIn("tools", payload)

    def test_knowledge_chat(self):
        before = len(self.upstream.requests)
        result = self.oai.chat.completions.create(model=KNOWLEDGE_MODEL, messages=[KNOWLEDGE_USER])
        self.assertEqual(len(self.upstream.requests), before + 1)
        self.assertEqual(result.model, KNOWLEDGE_MODEL)
        self.assertEqual(result.choices[0].finish_reason, "stop")
        self.assertEqual(result.usage.total_tokens, 11)
        self.assert_knowledge_answer(result.choices[0].message.content)

    def test_knowledge_chat_stream(self):
        with self.oai.chat.completions.create(
            model=KNOWLEDGE_MODEL, messages=[KNOWLEDGE_USER], stream=True,
            stream_options={"include_usage": True},
        ) as stream:
            chunks = list(stream)
        text = "".join(c.choices[0].delta.content or "" for c in chunks if c.choices)
        self.assert_knowledge_answer(text)
        self.assertTrue(all(c.model == KNOWLEDGE_MODEL for c in chunks))
        footer_index = next(i for i, c in enumerate(chunks)
                            if c.choices and "Sources:" in (c.choices[0].delta.content or ""))
        finish_index = next(i for i, c in enumerate(chunks)
                            if c.choices and c.choices[0].finish_reason is not None)
        self.assertLess(footer_index, finish_index)
        self.assertEqual(chunks[finish_index].choices[0].finish_reason, "stop")
        self.assertEqual(chunks[-1].usage.total_tokens, 11)

    def test_knowledge_responses(self):
        result = self.oai.responses.create(model=KNOWLEDGE_MODEL, input=KNOWLEDGE_QUERY, store=False)
        self.assertEqual(result.model, KNOWLEDGE_MODEL)
        self.assertEqual(result.status, "completed")
        self.assert_knowledge_answer(result.output_text)

    def test_knowledge_responses_stream(self):
        with self.oai.responses.stream(model=KNOWLEDGE_MODEL, input=KNOWLEDGE_QUERY, store=False) as stream:
            deltas = [event.delta for event in stream if event.type == "response.output_text.delta"]
            result = stream.get_final_response()
        self.assertEqual(result.model, KNOWLEDGE_MODEL)
        self.assertEqual(result.status, "completed")
        self.assertEqual("".join(deltas), result.output_text)
        self.assert_knowledge_answer(result.output_text)

    def test_knowledge_anthropic(self):
        result = self.claude.messages.create(model=KNOWLEDGE_MODEL, max_tokens=128, messages=[KNOWLEDGE_USER])
        self.assertEqual(result.model, KNOWLEDGE_MODEL)
        self.assertEqual(result.stop_reason, "end_turn")
        self.assert_knowledge_answer(result.content[0].text)

    def test_knowledge_anthropic_stream(self):
        with self.claude.messages.stream(model=KNOWLEDGE_MODEL, max_tokens=128, messages=[KNOWLEDGE_USER]) as stream:
            text = "".join(stream.text_stream)
            result = stream.get_final_message()
        self.assertEqual(result.model, KNOWLEDGE_MODEL)
        self.assertEqual(result.stop_reason, "end_turn")
        self.assertEqual(text, result.content[0].text)
        self.assert_knowledge_answer(text)

    def test_knowledge_no_match_skips_upstream(self):
        before = len(self.upstream.requests)
        result = self.oai.chat.completions.create(
            model=KNOWLEDGE_MODEL, messages=[{"role": "user", "content": "quasarxyz"}],
        )
        self.assertEqual(result.model, KNOWLEDGE_MODEL)
        self.assertIn("No relevant documents", result.choices[0].message.content)
        self.assertNotIn("Sources:", result.choices[0].message.content)
        self.assertEqual(len(self.upstream.requests), before)

    def test_knowledge_fallback_across_sdks(self):
        question = "은하항해xyz 설명해줘"
        user = {"role": "user", "content": question}
        notice = "지식 베이스에서 관련 문서를 찾지 못해 일반 지식으로 답변합니다."
        for protocol in ("chat", "responses", "anthropic"):
            for streaming in (False, True):
                with self.subTest(protocol=protocol, streaming=streaming):
                    before = len(self.upstream.requests)
                    if protocol == "chat":
                        result = self.oai.chat.completions.create(
                            model=FALLBACK_MODEL, messages=[user], stream=streaming,
                        )
                        if streaming:
                            with result as stream:
                                text = "".join(c.choices[0].delta.content or "" for c in stream if c.choices)
                        else:
                            text = result.choices[0].message.content
                    elif protocol == "responses":
                        if streaming:
                            with self.oai.responses.stream(model=FALLBACK_MODEL, input=question, store=False) as stream:
                                text = "".join(e.delta for e in stream if e.type == "response.output_text.delta")
                                self.assertEqual(text, stream.get_final_response().output_text)
                        else:
                            text = self.oai.responses.create(model=FALLBACK_MODEL, input=question, store=False).output_text
                    elif streaming:
                        with self.claude.messages.stream(model=FALLBACK_MODEL, max_tokens=128, messages=[user]) as stream:
                            text = "".join(stream.text_stream)
                            self.assertEqual(text, stream.get_final_message().content[0].text)
                    else:
                        text = self.claude.messages.create(model=FALLBACK_MODEL, max_tokens=128, messages=[user]).content[0].text
                    self.assertEqual(text, notice + "\n\n" + TEXT)
                    self.assertEqual(len(self.upstream.requests), before + 1)
                    payload = self.upstream.requests[-1]
                    self.assertEqual(payload["model"], "upstream-model")
                    self.assertEqual(payload["messages"][-1], user)
                    self.assertNotIn(KNOWLEDGE_URL, json.dumps(payload))
                    self.assertNotIn("tools", payload)

    def test_knowledge_search_and_status(self):
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

        def request(path, body=None, token=TOKEN):
            data = None if body is None else json.dumps(body).encode()
            req = urllib.request.Request(self.base_url + path, data=data,
                                         headers={"Authorization": f"Bearer {token}",
                                                  "Content-Type": "application/json"})
            with opener.open(req, timeout=10) as response:
                self.assertEqual(response.status, 200)
                return json.load(response)

        before = len(self.upstream.requests)
        for name in (None, "default"):
            body = {"query": KNOWLEDGE_QUERY, "limit": 5}
            if name is not None:
                body["knowledge"] = name
            result = request("/v1/search", body)
            self.assertEqual(result["knowledge"], "default")
            self.assertEqual(len(result["data"]), 1)
            self.assertEqual(result["data"][0]["path"], "backup.md")
            self.assertIn(KNOWLEDGE_TEXT, result["data"][0]["text"])
        status = request("/v1/knowledge/status")
        self.assertEqual(len(status["data"]), 1)
        base = status["data"][0]
        self.assertEqual(base["id"], "default")
        self.assertTrue(base["ready"])
        self.assertFalse(base["refresh_failed"])
        self.assertEqual(base["documents"], 1)
        self.assertEqual(base["chunks"], 1)
        self.assertEqual(base["refresh_interval"], "30s")
        self.assertTrue(base["updated_at"])
        for path, body in (("/v1/search", {"query": KNOWLEDGE_QUERY}), ("/v1/knowledge/status", None)):
            with self.assertRaises(urllib.error.HTTPError) as error:
                request(path, body, token="wrong-token")
            self.assertEqual(error.exception.code, 401)
            error.exception.close()
        self.assertEqual(len(self.upstream.requests), before)

    def assert_image_order(self):
        parts = self.upstream.requests[-1]["messages"][-1]["content"]
        self.assertEqual([part["type"] for part in parts],
                         ["text", "image_url", "text", "image_url", "text"])
        self.assertEqual([parts[i]["text"] for i in (0, 2, 4)], ["Before", "Between", "After"])
        self.assertEqual([parts[i]["image_url"]["url"] for i in (1, 3)],
                         [IMAGE_DATA_URI, REMOTE_IMAGE])
        return parts

    def test_vision_chat(self):
        result = self.oai.chat.completions.create(model=VISION_MODEL, messages=[{
            "role": "user", "content": [
                {"type": "text", "text": "Before"},
                {"type": "image_url", "image_url": {"url": IMAGE_DATA_URI, "detail": "low"}},
                {"type": "text", "text": "Between"},
                {"type": "image_url", "image_url": {"url": REMOTE_IMAGE, "detail": "high"}},
                {"type": "text", "text": "After"},
            ],
        }])
        self.assertEqual(result.choices[0].message.content, TEXT)
        parts = self.assert_image_order()
        self.assertEqual([parts[i]["image_url"]["detail"] for i in (1, 3)], ["low", "high"])

    def test_vision_responses(self):
        result = self.oai.responses.create(model=VISION_MODEL, store=False, input=[{
            "role": "user", "content": [
                {"type": "input_text", "text": "Before"},
                {"type": "input_image", "image_url": IMAGE_DATA_URI, "detail": "low"},
                {"type": "input_text", "text": "Between"},
                {"type": "input_image", "image_url": REMOTE_IMAGE, "detail": "high"},
                {"type": "input_text", "text": "After"},
            ],
        }])
        self.assertEqual(result.output_text, TEXT)
        parts = self.assert_image_order()
        self.assertEqual([parts[i]["image_url"]["detail"] for i in (1, 3)], ["low", "high"])

    def test_vision_anthropic(self):
        result = self.claude.messages.create(model=VISION_MODEL, max_tokens=128, messages=[{
            "role": "user", "content": [
                {"type": "text", "text": "Before"},
                {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": PNG_BASE64}},
                {"type": "text", "text": "Between"},
                {"type": "image", "source": {"type": "url", "url": REMOTE_IMAGE}},
                {"type": "text", "text": "After"},
            ],
        }])
        self.assertEqual(result.content[0].text, TEXT)
        self.assert_image_order()

    def test_vision_streaming(self):
        with self.oai.chat.completions.stream(model=VISION_MODEL, messages=[{
            "role": "user", "content": [{"type": "image_url", "image_url": {"url": IMAGE_DATA_URI}}],
        }]) as stream:
            result = stream.get_final_completion()
        self.assertEqual(result.choices[0].message.content, TEXT)
        self.assertEqual(self.upstream.requests[-1]["messages"][0]["content"][0]["image_url"]["url"], IMAGE_DATA_URI)
        with self.oai.responses.stream(model=VISION_MODEL, store=False, input=[{
            "role": "user", "content": [{"type": "input_image", "image_url": IMAGE_DATA_URI}],
        }]) as stream:
            result = stream.get_final_response()
        self.assertEqual(result.output_text, TEXT)
        with self.claude.messages.stream(model=VISION_MODEL, max_tokens=128, messages=[{
            "role": "user", "content": [
                {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": PNG_BASE64}},
            ],
        }]) as stream:
            result = stream.get_final_message()
        self.assertEqual(result.content[0].text, TEXT)

    def test_image_capabilities_reject_before_upstream(self):
        before = len(self.upstream.paths)
        with self.assertRaises(openai.BadRequestError):
            self.oai.chat.completions.create(model=MODEL, messages=[{
                "role": "user", "content": [{"type": "image_url", "image_url": {"url": IMAGE_DATA_URI}}],
            }])
        with self.assertRaises(openai.BadRequestError):
            self.oai.responses.create(model=MODEL, store=False, input=[{
                "role": "user", "content": [{"type": "input_image", "image_url": IMAGE_DATA_URI}],
            }])
        with self.assertRaises(anthropic.BadRequestError):
            self.claude.messages.create(model=MODEL, max_tokens=128, messages=[{
                "role": "user", "content": [{"type": "image", "source": {"type": "url", "url": REMOTE_IMAGE}}],
            }])
        with self.assertRaises(openai.BadRequestError):
            self.oai.images.generate(model=MODEL, prompt="A test image")
        with self.assertRaises(openai.BadRequestError):
            self.oai.images.generate(model=VISION_MODEL, prompt="A test image")
        with self.assertRaises(openai.BadRequestError):
            self.oai.chat.completions.create(model=IMAGE_MODEL, messages=[USER])
        self.assertEqual(len(self.upstream.paths), before)

    def test_vision_invalid_sources(self):
        before = len(self.upstream.paths)
        for value in ["data:image/png;base64,not-base64!", "data:image/jpeg;base64," + PNG_BASE64,
                      "file:///tmp/private.png", "https://user:password@example.invalid/image.png"]:
            with self.subTest(image_url=value[:45]):
                with self.assertRaises(openai.BadRequestError):
                    self.oai.chat.completions.create(model=VISION_MODEL, messages=[{
                        "role": "user", "content": [{"type": "image_url", "image_url": {"url": value}}],
                    }])
        with self.assertRaises(openai.BadRequestError):
            self.oai.responses.create(model=VISION_MODEL, store=False, input=[{
                "role": "user", "content": [{"type": "input_image", "file_id": "file_not_supported"}],
            }])
        with self.assertRaises(anthropic.BadRequestError):
            self.claude.messages.create(model=VISION_MODEL, max_tokens=128, messages=[{
                "role": "user", "content": [
                    {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "invalid!"}},
                ],
            }])
        self.assertEqual(len(self.upstream.paths), before)

    def test_vision_role_and_count_limits(self):
        before = len(self.upstream.paths)
        image = {"type": "image_url", "image_url": {"url": IMAGE_DATA_URI}}
        with self.assertRaises(openai.BadRequestError):
            self.oai.chat.completions.create(model=VISION_MODEL, messages=[{"role": "assistant", "content": [image]}])
        with self.assertRaises(openai.BadRequestError):
            self.oai.chat.completions.create(model=VISION_MODEL, messages=[{"role": "user", "content": [image] * 17}])
        self.assertEqual(len(self.upstream.paths), before)

    def test_image_generation_base64(self):
        result = self.oai.images.generate(model=IMAGE_MODEL, prompt="A test image", n=2,
                                          size="1024x1024", quality="low", response_format="b64_json",
                                          output_format="png", background="opaque")
        self.assertEqual(len(result.data), 2)
        self.assertEqual(base64.b64decode(result.data[0].b64_json, validate=True), PNG_BYTES)
        self.assertEqual(result.data[0].revised_prompt, "A tiny test fixture.")
        self.assertEqual(result.usage.total_tokens, 20)
        self.assertEqual(result.usage.input_tokens_details.text_tokens, 9)
        self.assertEqual(self.upstream.paths[-1], "/v1/images/generations")
        payload = self.upstream.requests[-1]
        self.assertEqual(payload["model"], "upstream-image")
        self.assertEqual(payload["n"], 2)
        self.assertEqual(payload["size"], "1024x1024")
        self.assertEqual(payload["quality"], "low")
        self.assertEqual(payload["background"], "opaque")

    def test_image_generation_url(self):
        result = self.oai.images.generate(model=IMAGE_MODEL, prompt="A test image", response_format="url")
        self.assertEqual(result.data[0].url, REMOTE_IMAGE)
        self.assertIsNone(result.data[0].b64_json)

    def test_image_generation_validation(self):
        before = len(self.upstream.paths)
        for options in [{"prompt": " "}, {"n": 0}, {"n": 11}, {"size": "0x1024"},
                        {"output_compression": 101}, {"response_format": "not-supported"}]:
            with self.subTest(options=options):
                with self.assertRaises(openai.BadRequestError):
                    self.oai.images.generate(**{"model": IMAGE_MODEL, "prompt": "A test image", **options})
        with self.assertRaises(openai.BadRequestError):
            self.oai.images.generate(model=IMAGE_MODEL, prompt="A test image", stream=True)
        self.assertEqual(len(self.upstream.paths), before)

    def test_image_generation_auth_and_errors(self):
        before = len(self.upstream.paths)
        with openai_client(self.base_url, "wrong-token") as client:
            with self.assertRaises(openai.AuthenticationError):
                client.images.generate(model=IMAGE_MODEL, prompt="A test image")
        with self.assertRaises(openai.NotFoundError):
            self.oai.images.generate(model="unknown-image-model", prompt="A test image")
        self.assertEqual(len(self.upstream.paths), before)
        with self.assertRaises(openai.InternalServerError):
            self.oai.images.generate(model=IMAGE_MODEL, prompt="[upstream-failure]")
        with self.assertRaises(openai.InternalServerError):
            self.oai.images.generate(model=IMAGE_MODEL, prompt="[malformed-image]")

    def test_image_edits_unsupported(self):
        before = len(self.upstream.paths)
        with self.assertRaises(openai.NotFoundError):
            self.oai.images.edit(model=IMAGE_MODEL, prompt="Edit this image",
                                 image=("fixture.png", PNG_BYTES, "image/png"))
        self.assertEqual(len(self.upstream.paths), before)

    def test_chat_raw_stream(self):
        with self.oai.chat.completions.create(
            model=MODEL, messages=[USER], stream=True, stream_options={"include_usage": True}
        ) as stream:
            chunks = list(stream)
        self.assertEqual("".join(c.choices[0].delta.content or "" for c in chunks if c.choices), TEXT)
        self.assertEqual([c.choices[0].finish_reason for c in chunks if c.choices][-1], "stop")
        self.assertEqual(chunks[-1].choices, [])
        self.assertEqual(chunks[-1].usage.total_tokens, 11)
        self.assertEqual(len({c.id for c in chunks}), 1)
        self.assertTrue(all(c.model == MODEL for c in chunks))

    def test_chat_stream_accumulation(self):
        with self.oai.chat.completions.stream(model=MODEL, messages=[USER]) as stream:
            result = stream.get_final_completion()
        self.assertEqual(result.choices[0].message.content, TEXT)

    def test_responses(self):
        result = self.oai.responses.create(model=MODEL, input="Hello", store=False)
        self.assertEqual(result.output_text, TEXT)
        self.assertEqual(result.model, MODEL)
        self.assertEqual(result.status, "completed")
        self.assertEqual(result.usage.input_tokens, 7)

    def test_responses_raw_stream(self):
        with self.oai.responses.create(model=MODEL, input="Hello", store=False, stream=True) as stream:
            events = list(stream)
        self.assertEqual(events[0].type, "response.created")
        self.assertEqual(events[-1].type, "response.completed")
        self.assertEqual("".join(e.delta for e in events if e.type == "response.output_text.delta"), TEXT)
        self.assertEqual(events[-1].response.output_text, TEXT)
        sequence = [e.sequence_number for e in events]
        self.assertEqual(sequence, sorted(set(sequence)))

    def test_responses_stream_accumulation(self):
        with self.oai.responses.stream(model=MODEL, input="Hello", store=False) as stream:
            result = stream.get_final_response()
        self.assertEqual(result.output_text, TEXT)
        self.assertEqual(result.status, "completed")

    def test_responses_text_history(self):
        result = self.oai.responses.create(model=MODEL, input="Hello", store=False)
        history = [USER] + result.output
        history.append({"role": "user", "content": "Continue"})
        followup = self.oai.responses.create(model=MODEL, input=history, store=False)
        self.assertEqual(followup.output_text, TEXT)
        self.assertEqual([m["role"] for m in self.upstream.requests[-1]["messages"]],
                         ["user", "assistant", "user"])

    def test_anthropic(self):
        result = self.claude.messages.create(model=MODEL, max_tokens=128, messages=[USER])
        self.assertEqual(result.model, MODEL)
        self.assertEqual(result.content[0].text, TEXT)
        self.assertEqual(result.stop_reason, "end_turn")
        self.assertEqual(result.usage.input_tokens, 7)
        self.assertEqual(result.usage.output_tokens, 4)

    def test_anthropic_raw_stream(self):
        with self.claude.messages.create(model=MODEL, max_tokens=128, messages=[USER], stream=True) as stream:
            events = list(stream)
        self.assertEqual(events[0].type, "message_start")
        self.assertEqual(events[0].message.content, [])
        self.assertEqual(events[-1].type, "message_stop")
        self.assertEqual("".join(e.delta.text for e in events if e.type == "content_block_delta"), TEXT)
        final = next(e for e in events if e.type == "message_delta")
        self.assertEqual(final.delta.stop_reason, "end_turn")
        self.assertEqual(final.usage.output_tokens, 4)

    def test_anthropic_stream_accumulation(self):
        with self.claude.messages.stream(model=MODEL, max_tokens=128, messages=[USER]) as stream:
            text = "".join(stream.text_stream)
            result = stream.get_final_message()
        self.assertEqual(text, TEXT)
        self.assertEqual(result.content[0].text, TEXT)
        self.assertEqual(result.stop_reason, "end_turn")

    def test_anthropic_text_history(self):
        result = self.claude.messages.create(model=MODEL, max_tokens=128, messages=[USER])
        followup = self.claude.messages.create(model=MODEL, max_tokens=128, messages=[
            USER, {"role": "assistant", "content": result.content},
            {"role": "user", "content": "Continue"},
        ])
        self.assertEqual(followup.content[0].text, TEXT)

    def test_chat_tool_roundtrip(self):
        result = self.oai.chat.completions.create(model=MODEL, messages=[USER], tools=[CHAT_TOOL])
        call = result.choices[0].message.tool_calls[0]
        self.assertEqual(json.loads(call.function.arguments), {"city": "Seoul"})
        self.assertEqual(result.choices[0].finish_reason, "tool_calls")
        followup = self.oai.chat.completions.create(model=MODEL, tools=[CHAT_TOOL], messages=[
            USER, {"role": "assistant", "tool_calls": [call], "content": None},
            {"role": "tool", "tool_call_id": call.id, "content": "Sunny"},
        ])
        self.assertEqual(followup.choices[0].message.content, TEXT)
        self.assertEqual(self.upstream.requests[-1]["messages"][-1]["tool_call_id"], "call_test")

    def test_chat_tool_stream(self):
        with self.oai.chat.completions.stream(model=MODEL, messages=[USER], tools=[CHAT_TOOL]) as stream:
            result = stream.get_final_completion()
        call = result.choices[0].message.tool_calls[0]
        self.assertEqual(call.function.name, "look_up")
        self.assertEqual(json.loads(call.function.arguments), {"city": "Seoul"})

    def test_responses_tool_roundtrip(self):
        result = self.oai.responses.create(model=MODEL, input="Hello", tools=[RESPONSE_TOOL], store=False)
        call = next(item for item in result.output if item.type == "function_call")
        self.assertEqual(call.name, "look_up")
        self.assertEqual(json.loads(call.arguments), {"city": "Seoul"})
        followup = self.oai.responses.create(model=MODEL, tools=[RESPONSE_TOOL], store=False, input=[
            USER, call, {"type": "function_call_output", "call_id": call.call_id, "output": "Sunny"},
        ])
        self.assertEqual(followup.output_text, TEXT)
        self.assertEqual(self.upstream.requests[-1]["messages"][-1]["tool_call_id"], "call_test")

    def test_responses_tool_stream(self):
        with self.oai.responses.stream(model=MODEL, input="Hello", tools=[RESPONSE_TOOL], store=False) as stream:
            result = stream.get_final_response()
        call = next(item for item in result.output if item.type == "function_call")
        self.assertEqual(call.name, "look_up")
        self.assertEqual(json.loads(call.arguments), {"city": "Seoul"})

    def test_anthropic_tool_roundtrip(self):
        result = self.claude.messages.create(model=MODEL, max_tokens=128, messages=[USER], tools=[ANTHROPIC_TOOL])
        call = next(item for item in result.content if item.type == "tool_use")
        self.assertEqual(call.name, "look_up")
        self.assertEqual(call.input, {"city": "Seoul"})
        self.assertEqual(result.stop_reason, "tool_use")
        followup = self.claude.messages.create(model=MODEL, max_tokens=128, tools=[ANTHROPIC_TOOL], messages=[
            USER, {"role": "assistant", "content": [call]},
            {"role": "user", "content": [{"type": "tool_result", "tool_use_id": call.id, "content": "Sunny"}]},
        ])
        self.assertEqual(followup.content[0].text, TEXT)
        self.assertEqual(self.upstream.requests[-1]["messages"][-1]["tool_call_id"], "call_test")

    def test_anthropic_tool_stream(self):
        with self.claude.messages.stream(model=MODEL, max_tokens=128, messages=[USER], tools=[ANTHROPIC_TOOL]) as stream:
            result = stream.get_final_message()
        call = next(item for item in result.content if item.type == "tool_use")
        self.assertEqual(call.name, "look_up")
        self.assertEqual(call.input, {"city": "Seoul"})
        self.assertEqual(result.stop_reason, "tool_use")

    def test_length_finish_reasons(self):
        chat = self.oai.chat.completions.create(model=MODEL, messages=[USER], max_tokens=1)
        self.assertEqual(chat.choices[0].finish_reason, "length")
        response = self.oai.responses.create(model=MODEL, input="Hello", max_output_tokens=1, store=False)
        self.assertEqual(response.status, "incomplete")
        self.assertEqual(response.incomplete_details.reason, "max_output_tokens")
        message = self.claude.messages.create(model=MODEL, messages=[USER], max_tokens=1)
        self.assertEqual(message.stop_reason, "max_tokens")

    def test_http_parameter_translation(self):
        self.oai.chat.completions.create(model=MODEL, messages=[USER], max_completion_tokens=32,
                                         temperature=0.2, top_p=0.8, stop=["END"])
        payload = self.upstream.requests[-1]
        self.assertEqual(payload["max_tokens"], 32)
        self.assertEqual(payload["temperature"], 0.2)
        self.assertEqual(payload["top_p"], 0.8)
        self.assertEqual(payload["stop"], ["END"])
        self.oai.responses.create(model=MODEL, input="Hello", instructions="Be concise", store=False,
                                   max_output_tokens=64, temperature=0.3, top_p=0.9)
        payload = self.upstream.requests[-1]
        self.assertEqual(payload["messages"][0], {"role": "system", "content": "Be concise"})
        self.assertEqual(payload["max_tokens"], 64)
        self.assertEqual(payload["temperature"], 0.3)
        self.claude.messages.create(model=MODEL, messages=[USER], max_tokens=128,
                                    system="Be concise", stop_sequences=["END"],
                                    extra_body={"temperature": 0.4})
        payload = self.upstream.requests[-1]
        self.assertEqual(payload["messages"][0], {"role": "system", "content": "Be concise"})
        self.assertEqual(payload["max_tokens"], 128)
        self.assertEqual(payload["temperature"], 0.4)
        self.assertEqual(payload["stop"], ["END"])

    def test_length_stream_finish_reasons(self):
        with self.oai.chat.completions.create(model=MODEL, messages=[USER], max_tokens=1, stream=True) as stream:
            chunks = list(stream)
        self.assertEqual([c.choices[0].finish_reason for c in chunks if c.choices][-1], "length")
        # The SDK accumulator only returns response.completed; use the raw
        # stream to observe a valid response.incomplete terminal event.
        with self.oai.responses.create(model=MODEL, input="Hello", max_output_tokens=1,
                                       store=False, stream=True) as stream:
            events = list(stream)
        self.assertEqual(events[-1].type, "response.incomplete")
        response = events[-1].response
        self.assertEqual(response.status, "incomplete")
        self.assertEqual(response.incomplete_details.reason, "max_output_tokens")
        with self.claude.messages.stream(model=MODEL, messages=[USER], max_tokens=1) as stream:
            message = stream.get_final_message()
        self.assertEqual(message.stop_reason, "max_tokens")

    def test_authentication_errors(self):
        with openai_client(self.base_url, "wrong-token") as client:
            with self.assertRaises(openai.AuthenticationError):
                client.chat.completions.create(model=MODEL, messages=[USER])
        with anthropic_client(self.base_url, "wrong-token") as client:
            with self.assertRaises(anthropic.AuthenticationError) as error:
                client.messages.create(model=MODEL, max_tokens=128, messages=[USER])
        self.assertEqual(error.exception.body["error"]["type"], "authentication_error")

    def test_unknown_models(self):
        with self.assertRaises(openai.NotFoundError):
            self.oai.chat.completions.create(model="does-not-exist", messages=[USER])
        with self.assertRaises(anthropic.NotFoundError):
            self.claude.messages.create(model="does-not-exist", max_tokens=128, messages=[USER])

    def test_unsupported_features(self):
        with self.assertRaises(openai.BadRequestError):
            self.oai.responses.create(model=MODEL, input="Hello", previous_response_id="resp_unknown")
        with self.assertRaises(openai.BadRequestError):
            self.oai.responses.create(model=MODEL, input="Hello", store=True)
        with self.assertRaises(openai.BadRequestError):
            self.oai.chat.completions.create(model=MODEL, messages=[{
                "role": "user", "content": [{"type": "image_url", "image_url": {"url": "https://example.invalid/test.png"}}],
            }])
        with self.assertRaises(anthropic.BadRequestError):
            self.claude.messages.create(model=MODEL, max_tokens=128, messages=[USER],
                                        thinking={"type": "enabled", "budget_tokens": 1024})

    def test_upstream_failure(self):
        with self.assertRaises(openai.InternalServerError):
            self.oai.chat.completions.create(model=MODEL, messages=[{"role": "user", "content": "[upstream-failure]"}])
        with self.assertRaises(anthropic.InternalServerError):
            self.claude.messages.create(model=MODEL, max_tokens=128, messages=[{"role": "user", "content": "[upstream-failure]"}])

    def test_upstream_stream_failure(self):
        failure = {"role": "user", "content": "[upstream-failure]"}
        with self.assertRaises(openai.APIError):
            with self.oai.chat.completions.create(model=MODEL, messages=[failure], stream=True) as stream:
                list(stream)
        with self.assertRaises(anthropic.APIError):
            with self.claude.messages.create(model=MODEL, messages=[failure], max_tokens=128, stream=True) as stream:
                list(stream)
        with self.oai.responses.create(model=MODEL, input=[failure], store=False, stream=True) as stream:
            events = list(stream)
        self.assertEqual(events[-1].type, "response.failed")
        self.assertEqual(events[-1].response.status, "failed")
        self.assertEqual(events[-1].response.error.code, "server_error")


def available_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path, help="Path to the built clivoy executable")
    args = parser.parse_args()
    binary = args.binary.resolve()
    if not binary.is_file():
        parser.error(f"No binary at {binary}; build it with go build -o clivoy ./cmd/clivoy")
    print(f"Official SDKs: openai {openai.__version__}, anthropic {anthropic.__version__}", flush=True)
    upstream = LocalTestServer(("127.0.0.1", 0), FakeUpstream)
    upstream.daemon_threads = True
    upstream.requests = []
    upstream.paths = []
    worker = threading.Thread(target=upstream.serve_forever, daemon=True)
    worker.start()
    process = None
    try:
        with tempfile.TemporaryDirectory(prefix="clivoy-sdk-") as directory:
            directory = Path(directory)
            port = available_port()
            knowledge = directory / "knowledge"
            knowledge.mkdir()
            (knowledge / "backup.md").write_text(
                f"{KNOWLEDGE_URL}\n\n# {KNOWLEDGE_TITLE}\n\n{KNOWLEDGE_TEXT}\n", encoding="utf-8",
            )
            config = {
                "listen": f"127.0.0.1:{port}", "api_key_env": "CLIVOY_SDK_TEST_KEY",
                "request_timeout": "15s", "max_concurrent": 4, "max_queue": 8, "max_body_bytes": 1048576,
                "providers": {"test": {"type": "openai", "base_url": f"http://127.0.0.1:{upstream.server_port}/v1",
                                       "api_key_env": "CLIVOY_SDK_UPSTREAM_KEY"}},
                "knowledge_bases": {"default": {"root": "knowledge", "top_k": 10,
                                                 "max_context_tokens": 6000, "refresh_interval": "30s"}},
                "models": {
                    MODEL: {"provider": "test", "model": "upstream-model"},
                    VISION_MODEL: {"provider": "test", "model": "upstream-model", "capabilities": ["chat", "vision"]},
                    IMAGE_MODEL: {"provider": "test", "model": "upstream-image", "capabilities": ["image_generation"]},
                    KNOWLEDGE_MODEL: {"provider": "test", "model": "upstream-model",
                                      "knowledge": "default", "capabilities": ["chat"]},
                    FALLBACK_MODEL: {"provider": "test", "model": "upstream-model",
                                     "knowledge": "default", "knowledge_fallback": "model", "capabilities": ["chat"]},
                },
            }
            path = directory / "config.json"
            path.write_text(json.dumps(config), encoding="utf-8")
            env = {**os.environ, "CLIVOY_SDK_TEST_KEY": TOKEN, "CLIVOY_SDK_UPSTREAM_KEY": UPSTREAM_TOKEN,
                   "CLIVOY_HOME": str(directory / "home"),
                   "NO_PROXY": "localhost,127.0.0.1", "no_proxy": "localhost,127.0.0.1"}
            base = f"http://127.0.0.1:{port}"
            with (directory / "clivoy.log").open("w+", encoding="utf-8") as log:
                process = subprocess.Popen([str(binary), "serve", "--config", str(path)],
                                           env=env, stdout=log, stderr=subprocess.STDOUT)
                try:
                    deadline = time.monotonic() + 15
                    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
                    while True:
                        if process.poll() is not None:
                            raise RuntimeError("Clivoy exited before becoming healthy")
                        try:
                            with opener.open(base + "/healthz", timeout=0.5) as response:
                                if response.status == 200:
                                    break
                        except (urllib.error.URLError, TimeoutError):
                            pass
                        if time.monotonic() >= deadline:
                            raise RuntimeError("Clivoy did not become healthy within 15 seconds")
                        time.sleep(0.05)
                    SDKCompatibility.base_url = base
                    SDKCompatibility.upstream = upstream
                    result = unittest.TextTestRunner(verbosity=2).run(
                        unittest.defaultTestLoader.loadTestsFromTestCase(SDKCompatibility)
                    )
                    if not result.wasSuccessful():
                        log.flush()
                        log.seek(0)
                        print("\nClivoy log:\n" + log.read())
                    return 0 if result.wasSuccessful() else 1
                except Exception:
                    log.flush()
                    log.seek(0)
                    print("\nClivoy log:\n" + log.read())
                    raise
                finally:
                    process.terminate()
                    try:
                        process.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait(timeout=5)
    finally:
        upstream.shutdown()
        upstream.server_close()
        worker.join(timeout=5)


if __name__ == "__main__":
    raise SystemExit(main())
