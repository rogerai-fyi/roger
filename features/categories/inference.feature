# Slice 3 - best-effort inference: the node guesses what a model is for when the operator
# has not said, and labels the guess "inferred" so no surface presents it as a claim.
#
# The guess reads only what detection already has: the model id, the detected modality and
# the detected capabilities. It is ordered (first rule that matches wins) and deliberately
# conservative - a missing guess costs a filter result; a wrong one misleads a consumer.
# Medical and targeting categories do not exist (offer_taxonomy.feature), so no guess can
# produce one.
#
# GROUND TRUTH: internal/detect (modalityFromID, visionFromID / visionMarkers, and the
# capabilities classifyCapabilities fills).

Feature: The node infers a best-effort taxonomy from what detection already knows

  Scenario Outline: The model id, modality and capabilities give a category
    Given a detected model "<model>" with modality "<modality>" and capabilities "<caps>"
    When the node infers its taxonomy
    Then the inferred category is "<category>" and subcategory "<subcategory>"
    And the inferred claim source is "inferred"

    Examples:
      | model                         | modality | caps   | category  | subcategory |
      | kokoro                        | tts      |        | audio     | tts         |
      | whisper-large-v3              | stt      |        | audio     | stt         |
      | bge-reranker-v2-m3            | chat     |        | retrieval | rerank      |
      | jina-reranker-v2              | chat     |        | retrieval | rerank      |
      | nomic-embed-text              | chat     |        | retrieval | embeddings  |
      | bge-m3                        | chat     |        | retrieval | embeddings  |
      | e5-mistral-7b-instruct        | chat     |        | retrieval | embeddings  |
      | gte-qwen2-7b-instruct         | chat     |        | retrieval | embeddings  |
      | mxbai-embed-large             | chat     |        | retrieval | embeddings  |
      | all-MiniLM-L6-v2              | chat     |        | retrieval | embeddings  |
      | got-ocr2                      | chat     |        | vision    | ocr         |
      | olmOCR-7B                     | chat     | vision | vision    | ocr         |
      | nanonets-ocr-s                | chat     |        | vision    | ocr         |
      | docling                       | chat     |        | vision    | document    |
      | layoutlmv3-base               | chat     |        | vision    | document    |
      | yolov8n                       | chat     |        | vision    | detection   |
      | qwen3-vl-8b                   | chat     |        | vision    | vlm         |
      | some-model                    | chat     | vision | vision    | vlm         |
      | qwen3-coder-30b-a3b           | chat     |        | language  | code        |
      | codestral-22b                 | chat     |        | language  | code        |
      | devstral-small                | chat     |        | language  | code        |
      | starcoder2-15b                | chat     |        | language  | code        |
      | gpt-oss-20b                   | chat     |        | language  | chat        |
      | llama3.2                      | chat     |        | language  | chat        |
      | GPT-OSS-120B                  | chat     |        | language  | chat        |
      # Regressions (slice-3 review): a cross-encoder is a reranker; "ocr" inside a word,
      # and "coder" inside encoder/decoder, are not hints; more code families.
      | cross-encoder/ms-marco-MiniLM-L-6-v2 | chat |     | retrieval | rerank      |
      | socratic-7b                   | chat     |        | language  | chat        |
      | flan-t5-encoder               | chat     |        | language  | chat        |
      | bart-large-decoder            | chat     |        | language  | chat        |
      | paddleocr                     | chat     |        | vision    | ocr         |
      | codeqwen1.5-7b                | chat     |        | language  | code        |
      | granite-code-8b               | chat     |        | language  | code        |
      | stable-code-3b                | chat     |        | language  | code        |

  # Order: a reranker named after an embedding family is a reranker; an OCR model that also
  # sees images is OCR; a coder that sees images is a vision model.
  Scenario Outline: The first matching rule wins
    Given a detected model "<model>" with modality "<modality>" and capabilities "<caps>"
    When the node infers its taxonomy
    Then the inferred category is "<category>" and subcategory "<subcategory>"

    Examples:
      | model                 | modality | caps   | category  | subcategory |
      | bge-reranker-large    | chat     |        | retrieval | rerank      |
      | olmocr-vl             | chat     | vision | vision    | ocr         |
      | qwen2.5-vl-coder      | chat     |        | vision    | vlm         |
      | whisper-embed         | stt      |        | audio     | stt         |

  Scenario Outline: A name that says the refusals were removed infers the uncensored grade
    Given a detected model "<model>" with modality "chat" and capabilities ""
    When the node infers its taxonomy
    Then the inferred content policy is "<policy>"

    Examples:
      | model                                | policy     |
      | Huihui-Qwen3-8B-abliterated-v2       | uncensored |
      | dolphin-2.9-llama3-8b-uncensored     | uncensored |
      | llama3.2                             |            |
      | censorship-classifier                |            |

  Scenario: Inference never invents tags, tuning, lineage or a license
    Given a detected model "qwen3-coder-30b-a3b" with modality "chat" and capabilities ""
    When the node infers its taxonomy
    Then the inferred taxonomy has no tags, tuning, base model or license

  Scenario: An empty model id infers nothing
    Given a detected model "" with modality "chat" and capabilities ""
    When the node infers its taxonomy
    Then the inferred taxonomy is empty

  Scenario: The inferred taxonomy is already canonical
    Given a detected model "Nomic-Embed-Text" with modality "chat" and capabilities ""
    When the node infers its taxonomy
    Then normalizing the inferred taxonomy changes nothing

  # Detection fills it per model, beside quant/weights/variant, so every share path gets it.
  Scenario: Detection records the inferred taxonomy for each served model
    Given a local server that serves "nomic-embed-text" and "llama3.2"
    When the node scans for local models
    Then the scan's inferred taxonomy for "nomic-embed-text" is "retrieval/embeddings"
    And the scan's inferred taxonomy for "llama3.2" is "language/chat"
