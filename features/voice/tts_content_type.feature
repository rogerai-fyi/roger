# TTS RESPONSE CONTENT-TYPE HONESTY (2026-07-02 live incident): every 200 from
# /v1/audio/speech carried a STATIC `audio/mpeg` (audioSpec.contentType), even when
# the station actually returned WAV - Kokoro's default - so players/browsers keying
# on the header mis-decoded or refused the audio.
#
# THE CONTRACT: the Content-Type follows the ACTUAL BYTES first (the station may
# ignore response_format; the header must describe what we really return), then the
# REQUESTED response_format, then the historical audio/mpeg default:
#   RIFF/WAVE bytes            -> audio/wav
#   ID3 / MPEG-sync bytes      -> audio/mpeg
#   unrecognized + "wav" asked -> audio/wav
#   unrecognized + "mp3" asked -> audio/mpeg
#   unrecognized + nothing     -> audio/mpeg   (unchanged default)
# The transcription relay is untouched: STT results stay application/json.
#
# GROUND TRUTH: cmd/rogerai-broker/audio.go - audioRelayCore's 200 write path
# (spec.contentType) + the TTS audioSpec in audioRelay; ttsContentType/sniffed
# formats once implemented.
#
# Enforced by: cmd/rogerai-broker/audio_content_type_bdd_test.go (the REAL
# audioRelay money path against a stub station returning controlled bytes - the
# tts_metering harness pattern; no mocks).

Feature: TTS responses carry the content type of the audio actually returned

  Scenario: A WAV result requested as wav is served as audio/wav
    Given a tts station whose result bytes are a WAV clip
    When a funded consumer requests speech with response_format "wav"
    Then the response is 200 with content type "audio/wav"

  Scenario: An MP3 result requested as mp3 is served as audio/mpeg
    Given a tts station whose result bytes are an MP3 clip
    When a funded consumer requests speech with response_format "mp3"
    Then the response is 200 with content type "audio/mpeg"

  # The incident: Kokoro ignored response_format and returned its WAV default.
  Scenario: The actual bytes win when the station ignores the requested format
    Given a tts station whose result bytes are a WAV clip
    When a funded consumer requests speech with response_format "mp3"
    Then the response is 200 with content type "audio/wav"

  Scenario: Unrecognizable bytes fall back to the requested format
    Given a tts station whose result bytes are unrecognizable
    When a funded consumer requests speech with response_format "wav"
    Then the response is 200 with content type "audio/wav"

  Scenario: Unrecognizable bytes with no requested format keep the audio/mpeg default
    Given a tts station whose result bytes are unrecognizable
    When a funded consumer requests speech with no response_format
    Then the response is 200 with content type "audio/mpeg"

  Scenario: Transcription results stay application/json
    Given an stt station whose result is a transcription
    When a funded consumer posts audio for transcription
    Then the response is 200 with content type "application/json"
