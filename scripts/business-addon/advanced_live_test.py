"""Socket-free Python helper/SDK serialization checks, not Go handler tests."""
import copy
import json
import unittest
from unittest.mock import patch

import httpx
from google import genai
from google.genai import errors, types

from advanced_live import (COUNT_TEXT, MODEL, MODEL_PATH, ORIGIN,
                           EndpointGuard, GuardFailure, collect, compact, grounding_receipt)


class AdvancedRunnerTests(unittest.TestCase):
    @staticmethod
    def late_grounding_frames():
        first = '中文说明：代码行长79。'
        second = '🙂依据官方规范。'
        segments = [
            {'partIndex': 0, 'startIndex': len('中文说明：'.encode()),
             'endIndex': len('中文说明：代码行长79'.encode()), 'text': '代码行长79'},
            {'partIndex': 1, 'startIndex': 0, 'endIndex': len('🙂'.encode()), 'text': '🙂'},
        ]
        return [
            {'candidates': [{'content': {'role': 'model', 'parts': [{'text': first}]}}]},
            {'candidates': [{'content': {'role': 'model', 'parts': [{'text': second}]}}]},
            {'candidates': [{'content': {'role': 'model', 'parts': [{'inlineData': {
                'mimeType': 'image/gif', 'data': 'R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw=='}}]},
                'finishReason': 'STOP', 'groundingMetadata': {
                    'groundingChunks': [{'retrievedContext': {'uri': 'https://peps.python.org/pep-0008/', 'title': 'PEP 8'}}],
                    'groundingSupports': [{'segment': segment, 'groundingChunkIndices': [0]} for segment in segments]}}]},
        ]

    def collect_mock_stream(self, frames):
        """Pass malformed fixture frames through the pinned SDK, without sockets."""
        guard = EndpointGuard()
        guard.stage = 'stream_turn'
        requests = []

        def respond(request):
            requests.append(request.url.path)
            return httpx.Response(200, headers={'Content-Type': 'text/event-stream'},
                content=b''.join(b'data: ' + compact(frame) + b'\n\n' for frame in frames))

        http = httpx.Client(transport=httpx.MockTransport(respond), follow_redirects=False,
            event_hooks={'request': [guard.request], 'response': [guard.response]})
        client = genai.Client(api_key='synthetic-offline', vertexai=False, http_options=types.HttpOptions(
            base_url=ORIGIN, api_version='v1beta', httpx_client=http,
            retry_options=types.HttpRetryOptions(attempts=1)))
        try:
            stream = client.models.generate_content_stream(model=MODEL, contents='synthetic local fixture',
                config=types.GenerateContentConfig(tools=[types.Tool(google_search=types.GoogleSearch())],
                    temperature=0, max_output_tokens=64,
                    thinking_config=types.ThinkingConfig(include_thoughts=False, thinking_budget=0),
                    automatic_function_calling=types.AutomaticFunctionCallingConfig(disable=True),
                    automatic_continuation=False))
            return collect(stream, 0)
        finally:
            self.assertEqual(requests, [MODEL_PATH + ':streamGenerateContent'])
            self.assertEqual(guard.generation_attempts, 1)
            client.close()
            http.close()

    def test_pinned_sdk_serialization_and_request_bounds(self):
        guard = EndpointGuard()
        seen = []

        def respond(request):
            seen.append(request.url.path)
            if guard.stage in guard.preflights:
                return httpx.Response(400, json={'error': {'code': 400, 'status': 'INVALID_ARGUMENT', 'message': 'synthetic'}})
            if request.url.path.endswith(':countTokens'):
                return httpx.Response(501, json={'error': {'code': 501, 'status': 'UNIMPLEMENTED', 'message': 'synthetic'}})
            if request.url.path.endswith(':streamGenerateContent'):
                frames = [
                    {'candidates': [{'content': {'role': 'model', 'parts': [{'text': 'first '}]}}]},
                    {'candidates': [{'content': {'role': 'model', 'parts': [{'text': 'answer'}]}, 'finishReason': 'STOP'}]},
                ]
                return httpx.Response(200, headers={'Content-Type': 'text/event-stream'},
                    content=b''.join(b'data: ' + compact(frame) + b'\n\n' for frame in frames))
            return httpx.Response(200, json={'candidates': [{'content': {'role': 'model', 'parts': [{'text': 'second answer'}]}, 'finishReason': 'STOP'}]})

        http = httpx.Client(transport=httpx.MockTransport(respond), follow_redirects=False,
            event_hooks={'request': [guard.request], 'response': [guard.response]})
        client = genai.Client(api_key='synthetic-offline', vertexai=False, http_options=types.HttpOptions(
            base_url=ORIGIN, api_version='v1beta', httpx_client=http,
            retry_options=types.HttpRetryOptions(attempts=1)))
        try:
            for name, body in guard.preflights.items():
                guard.stage = name
                self.assertEqual(http.post(ORIGIN + MODEL_PATH + ':generateContent', content=body).status_code, 400)
            guard.stage = 'count_tokens'
            with self.assertRaises(errors.APIError) as failed:
                client.models.count_tokens(model=MODEL, contents=COUNT_TEXT)
            self.assertEqual(failed.exception.code, 501)
            config = types.GenerateContentConfig(tools=[types.Tool(google_search=types.GoogleSearch())],
                temperature=0, max_output_tokens=64,
                thinking_config=types.ThinkingConfig(include_thoughts=False, thinking_budget=0),
                automatic_function_calling=types.AutomaticFunctionCallingConfig(disable=True), automatic_continuation=False)
            chat = client.chats.create(model=MODEL, config=config)
            guard.stage = 'stream_turn'
            list(chat.send_message_stream('synthetic first turn'))
            guard.stage = 'continuation_turn'
            chat.send_message('synthetic second turn')
            self.assertEqual(guard.generation_attempts, 2)
            self.assertEqual(guard.count_attempts, 1)
            self.assertTrue(guard.tools_unchanged and guard.history_present)
            with self.assertRaises(GuardFailure):
                chat.send_message('forbidden third generation')
            self.assertEqual(len(seen), 9)
            self.assertEqual(guard.generation_http, [200, 200])
        finally:
            client.close()
            http.close()

    def test_guard_rejects_other_origins_and_status_credentials(self):
        guard = EndpointGuard()
        for address in ('https://127.0.0.1:2048/api/addons/business/status',
                        'http://localhost:2048/api/addons/business/status',
                        'http://127.0.0.1:2049/api/addons/business/status',
                        'http://127.0.0.1:2048/other'):
            with self.assertRaises(GuardFailure):
                guard.request(httpx.Request('GET', address))
        with self.assertRaises(GuardFailure):
            guard.request(httpx.Request('GET', ORIGIN + '/api/addons/business/status', headers={'x-goog-api-key': 'synthetic'}))
        self.assertEqual(guard.status_attempts, 0)

    def test_grounding_validates_utf8_offsets_and_official_source(self):
        parts = [types.Part(text='引言'), types.Part(text='行数79。')]
        metadata = types.GroundingMetadata(grounding_chunks=[types.GroundingChunk(
            retrieved_context=types.GroundingChunkRetrievedContext(uri='https://peps.python.org/pep-0008/', title='PEP 8'))],
            grounding_supports=[types.GroundingSupport(grounding_chunk_indices=[0],
                segment=types.Segment(part_index=1, start_index=6, end_index=8, text='79'))])
        result = grounding_receipt(metadata, parts)
        self.assertTrue(result['all_supports_valid'])
        self.assertEqual(result['official_pep8_supports'], 1)
        metadata.grounding_supports[0].segment.start_index = 5
        result = grounding_receipt(metadata, parts)
        self.assertFalse(result['all_supports_valid'])
        self.assertEqual(result['valid_supports'], 0)
        self.assertEqual(result['official_pep8_supports'], 0)

    def test_collection_measures_distinct_text_frames(self):
        chunks = [
            types.GenerateContentResponse(candidates=[types.Candidate(content=types.Content(
                role='model', parts=[types.Part(text='first '), types.Part(text='chunk ')]))]),
            types.GenerateContentResponse(candidates=[types.Candidate(content=types.Content(
                role='model', parts=[types.Part(text='last')]), finish_reason='STOP',
                grounding_metadata=types.GroundingMetadata(grounding_chunks=[types.GroundingChunk(
                    retrieved_context=types.GroundingChunkRetrievedContext(uri='https://peps.python.org/pep-0008/'))]))]),
        ]
        with patch('advanced_live.time.monotonic', side_effect=[11.0, 13.5, 15.0]):
            text, parts, receipt = collect(chunks, 10.0)
        self.assertEqual(text, 'first chunk last')
        self.assertEqual(len(parts), 3)
        self.assertEqual(receipt['text_events'], 2)
        self.assertEqual(receipt['first_text_seconds'], 1.0)
        self.assertEqual(receipt['text_arrival_span_seconds'], 2.5)
        self.assertEqual(receipt['elapsed_seconds'], 5.0)
        self.assertEqual(receipt['grounding_evidence'], 'source_only')
        self.assertEqual(receipt['valid_supports'], 0)

    def test_late_grounding_and_terminal_media_survive_sdk_chat(self):
        guard = EndpointGuard()
        requests = []
        frames = self.late_grounding_frames()

        def respond(request):
            requests.append(json.loads(request.content))
            if request.url.path.endswith(':streamGenerateContent'):
                return httpx.Response(200, headers={'Content-Type': 'text/event-stream'},
                    content=b''.join(b'data: ' + compact(frame) + b'\n\n' for frame in frames))
            return httpx.Response(200, json={'candidates': [{'content': {'role': 'model',
                'parts': [{'text': 'synthetic continuation'}]}, 'finishReason': 'STOP'}]})

        http = httpx.Client(transport=httpx.MockTransport(respond), follow_redirects=False,
            event_hooks={'request': [guard.request], 'response': [guard.response]})
        client = genai.Client(api_key='synthetic-offline', vertexai=False, http_options=types.HttpOptions(
            base_url=ORIGIN, api_version='v1beta', httpx_client=http,
            retry_options=types.HttpRetryOptions(attempts=1)))
        try:
            config = types.GenerateContentConfig(tools=[types.Tool(google_search=types.GoogleSearch())],
                temperature=0, max_output_tokens=64,
                thinking_config=types.ThinkingConfig(include_thoughts=False, thinking_budget=0),
                automatic_function_calling=types.AutomaticFunctionCallingConfig(disable=True), automatic_continuation=False)
            chat = client.chats.create(model=MODEL, config=config)
            guard.stage = 'stream_turn'
            text, parts, receipt = collect(chat.send_message_stream('synthetic late grounding first turn'), 0)
            self.assertEqual(text, '中文说明：代码行长79。🙂依据官方规范。')
            self.assertEqual(receipt['events'], 3)
            self.assertEqual(receipt['text_events'], 2)
            self.assertEqual(receipt['terminal_parts'], 1)
            self.assertEqual(receipt['valid_supports'], 2)
            self.assertEqual(receipt['official_pep8_supports'], 2)
            self.assertEqual(receipt['grounding_evidence'], 'source_and_valid_support')
            self.assertTrue(receipt['all_supports_valid'] and receipt['metadata_only_at_stop'] and receipt['stop_observed'])
            self.assertEqual(parts[-1].inline_data.mime_type, 'image/gif')
            self.assertTrue(parts[-1].inline_data.data.startswith(b'GIF89a'))
            history = chat.get_history(curated=True)
            self.assertEqual([part for content in history[1:] for part in content.parts or []], parts)
            guard.stage = 'continuation_turn'
            chat.send_message('synthetic continuation without restating first response')
            self.assertEqual(len(requests), 2)
            self.assertEqual(guard.generation_attempts, 2)
            self.assertTrue(guard.history_present and guard.tools_unchanged)
            sent_contents = requests[1]['contents']
            self.assertEqual(sent_contents[0], requests[0]['contents'][0])
            self.assertTrue(all(set(content) == {'role', 'parts'} for content in sent_contents))
            self.assertEqual([types.Part.model_validate(part) for content in sent_contents[1:-1]
                              for part in content['parts']], parts)
            sent_history = json.dumps(sent_contents)
            for metadata_key in ('groundingMetadata', 'grounding_metadata', 'citationMetadata', 'citation_metadata'):
                self.assertNotIn(metadata_key, sent_history)
        finally:
            client.close()
            http.close()

    def test_late_grounding_wrong_part_index_is_invalid(self):
        frames = self.late_grounding_frames()
        frames[-1]['candidates'][0]['groundingMetadata']['groundingSupports'][0]['segment']['partIndex'] = 3
        _, _, receipt = self.collect_mock_stream(frames)
        self.assertFalse(receipt['all_supports_valid'])
        self.assertEqual(receipt['valid_supports'], 1)

    def test_late_grounding_utf8_split_is_invalid(self):
        frames = self.late_grounding_frames()
        frames[-1]['candidates'][0]['groundingMetadata']['groundingSupports'][1]['segment']['startIndex'] = 1
        _, _, receipt = self.collect_mock_stream(frames)
        self.assertFalse(receipt['all_supports_valid'])
        self.assertEqual(receipt['valid_supports'], 1)

    def test_late_grounding_early_metadata_is_invalid(self):
        frames = self.late_grounding_frames()
        frames[0]['candidates'][0]['groundingMetadata'] = frames[-1]['candidates'][0].pop('groundingMetadata')
        _, _, receipt = self.collect_mock_stream(frames)
        self.assertTrue(receipt['all_supports_valid'])
        self.assertFalse(receipt['metadata_only_at_stop'])

    def test_late_grounding_duplicate_metadata_is_rejected(self):
        frames = self.late_grounding_frames()
        frames[0]['candidates'][0]['groundingMetadata'] = copy.deepcopy(frames[-1]['candidates'][0]['groundingMetadata'])
        with self.assertRaises(ValueError):
            self.collect_mock_stream(frames)

    def test_late_grounding_frames_after_stop_are_rejected(self):
        frames = self.late_grounding_frames()
        frames.append(copy.deepcopy(frames[0]))
        with self.assertRaises(ValueError):
            self.collect_mock_stream(frames)


if __name__ == '__main__':
    unittest.main()
