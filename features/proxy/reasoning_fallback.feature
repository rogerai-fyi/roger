Feature: Proxy reasoning->content fallback (relay path)
  Some reasoning models emit their final answer in the message.reasoning (or
  reasoning_content) channel and leave message.content EMPTY. Strict OpenAI clients
  (e.g. hermes -z) read only content, see it empty, and report "no final response -
  failed". The local relay proxy surfaces the reasoning AS content when, and only when,
  content is genuinely empty, so every client on a reasoning-heavy band gets a usable
  reply. This ONLY reshapes the response body text: it never changes billing, model,
  routing, or the SSE cost-meter comment, which pass through untouched.

  Founder ruling (option A, 2026-07-08): the fallback is ON by default. Because we fill
  only EMPTY content, a client that reads reasoning separately still gets the reasoning
  field intact AND now a content mirror - the accepted double-mirror tradeoff. A caller
  that needs raw passthrough can disable it per session.

  # NON-STREAMING (application/json): the whole body is buffered, transformed, forwarded.
  Rule: Non-streaming - empty content is filled from reasoning

    Scenario: Empty content plus reasoning surfaces the reasoning as content
      Given a tuned band whose model is "gpt-oss-120b"
      And the local proxy is bound to that band
      And the broker returns message content "" and reasoning "the answer is 42"
      When a chat request is made
      Then the status is 200
      And the reply content is "the answer is 42"

    Scenario: Whitespace-only content counts as empty and is filled
      Given a tuned band whose model is "gpt-oss-120b"
      And the local proxy is bound to that band
      And the broker returns message content "   \n" and reasoning "42"
      When a chat request is made
      Then the reply content is "42"

    Scenario: The reasoning_content field name is also honored
      Given a tuned band whose model is "gpt-oss-120b"
      And the local proxy is bound to that band
      And the broker returns message content "" and reasoning_content "deepseek says hi"
      When a chat request is made
      Then the reply content is "deepseek says hi"

    Scenario: The reasoning field is preserved intact (the double-mirror tradeoff)
      Given a tuned band whose model is "gpt-oss-120b"
      And the local proxy is bound to that band
      And the broker returns message content "" and reasoning "kept"
      When a chat request is made
      Then the reply content is "kept"
      And the reply reasoning is still "kept"

  Rule: Non-streaming - real content is never overwritten and never duplicated

    Scenario: Non-empty content is left exactly as the provider sent it
      Given a tuned band whose model is "gpt-oss-120b"
      And the local proxy is bound to that band
      And the broker returns message content "real answer" and reasoning "hidden thinking"
      When a chat request is made
      Then the reply content is "real answer"
      And the response body is byte-for-byte the broker's body

    Scenario: Empty content AND empty reasoning stays empty (nothing to surface)
      Given a tuned band whose model is "gpt-oss-120b"
      And the local proxy is bound to that band
      And the broker returns message content "" and reasoning ""
      When a chat request is made
      Then the reply content is empty
      And the response body is byte-for-byte the broker's body

  Rule: Non-streaming - the money path is untouched

    Scenario: The billed cost header passes through unchanged while content is filled
      Given a tuned band whose model is "gpt-oss-120b"
      And the local proxy is bound to that band
      And the broker bills "0.0025" for the next non-streaming reply
      And the broker returns message content "" and reasoning "billed answer"
      When a chat request is made
      Then the reply content is "billed answer"
      And the response carries cost header "0.0025"

  # STREAMING (text/event-stream): all chunks pass through live and byte-for-byte; if the
  # stream ends having emitted reasoning deltas but ZERO content, one synthesized content
  # delta carrying the accumulated reasoning is injected immediately before [DONE]. v1
  # limitation: the reasoning is delivered as a single consolidated delta at stream end,
  # not re-chunked live as it arrives.
  Rule: Streaming - an all-reasoning stream delivers the reasoning as content

    Scenario: Reasoning-only deltas are surfaced as a synthesized content delta
      Given a tuned band whose model is "gpt-oss-120b"
      And the local proxy is bound to that band
      And the broker streams reasoning deltas "the ans" "wer is 42" and no content
      When a chat request is made
      Then the status is 200
      And the streamed content is "the answer is 42"

    Scenario: The synthesized content is delivered BEFORE the finish_reason chunk
      # a strict client that finalizes on finish_reason must still see the content
      Given a tuned band whose model is "gpt-oss-120b"
      And the local proxy is bound to that band
      And the broker streams reasoning deltas "answer" and no content
      When a chat request is made
      Then the streamed content is "answer"
      And the synthesized content precedes the finish_reason chunk

  Rule: A tool-call turn's empty content is left intact (never filled)

    Scenario: Non-streaming - an empty-content reply carrying tool_calls is not filled
      Given a tuned band whose model is "gpt-oss-120b"
      And the local proxy is bound to that band
      And the broker returns a tool-call reply with empty content and reasoning "calling weather api"
      When a chat request is made
      Then the reply content is empty
      And the response body is byte-for-byte the broker's body

    Scenario: Streaming - a reasoning+tool_call stream gets no synthesized content delta
      Given a tuned band whose model is "gpt-oss-120b"
      And the local proxy is bound to that band
      And the broker streams a reasoning delta "thinking" then a tool_call and no content
      When a chat request is made
      Then the streamed content is ""
      And no content delta was synthesized

    Scenario: The original reasoning deltas still pass through (the double-mirror tradeoff)
      Given a tuned band whose model is "gpt-oss-120b"
      And the local proxy is bound to that band
      And the broker streams reasoning deltas "the ans" "wer is 42" and no content
      When a chat request is made
      Then the streamed reasoning is still "the answer is 42"

  Rule: Streaming - a stream with content is never touched or duplicated

    Scenario: A normal content stream is delivered verbatim with no synthesized delta
      Given a tuned band whose model is "gpt-oss-120b"
      And the local proxy is bound to that band
      And the broker streams content deltas "Hello" " world"
      When a chat request is made
      Then the streamed content is "Hello world"
      And no content delta was synthesized

  Rule: Streaming - the SSE cost-meter comment passes through untouched

    Scenario: The rogerai-cost meter comment survives a synthesized-content stream
      Given a tuned band whose model is "gpt-oss-120b"
      And the local proxy is bound to that band
      And the stream will bill "0.0009"
      And the broker streams reasoning deltas "done" and no content
      When a chat request is made
      Then the streamed content is "done"
      And the SSE cost-meter comment for "0.0009" is present

  Rule: The fallback is toggleable and defaults ON

    Scenario: Disabled - a non-streaming empty-content reply passes through raw
      Given a tuned band whose model is "gpt-oss-120b"
      And the reasoning fallback is disabled
      And the local proxy is bound to that band
      And the broker returns message content "" and reasoning "not surfaced"
      When a chat request is made
      Then the reply content is empty
      And the response body is byte-for-byte the broker's body

    Scenario: Disabled - a reasoning-only stream passes through raw (no synthesized delta)
      Given a tuned band whose model is "gpt-oss-120b"
      And the reasoning fallback is disabled
      And the local proxy is bound to that band
      And the broker streams reasoning deltas "hidden" and no content
      When a chat request is made
      Then the streamed content is ""
      And no content delta was synthesized
