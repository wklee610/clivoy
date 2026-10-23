<div align="center">

[English](README.md) | [한국어](README-ko.md)

# CLIVOY: CLI + Envoy

**내 AI CLI를 하나의 간단한 API로.**

</div>

Clivoy는 **Codex, Claude, Gemini, Grok, Ollama**를 하나의 로컬 API로 사용할 수 있게 해줍니다.
기존 CLI 로그인을 그대로 사용하고, 내 Ollama 서버에 연결합니다.
**xAI API**나 다른 호환 모델 서버도 연결할 수 있습니다.
OpenAI 또는 Anthropic SDK에서 서버 주소와 모델 이름만 지정하면 됩니다.
Markdown RAG를 설정하면 같은 API로 내 문서나 Obsidian 노트에 근거한 답변도 받을 수 있습니다.

Clivoy 실행에는 GPU나 Python이 필요 없습니다. macOS, Linux, Windows에서
실행하는 하나의 Go 프로그램입니다.

## Installation Guide

**macOS·Linux — Homebrew:**

```sh
brew install wklee610/clivoy/clivoy
```

**Go 1.25 이상 — macOS·Linux·Windows:**

```sh
go install github.com/wklee610/clivoy/cmd/clivoy@latest
```

Go 실행 파일 설치 경로를 `PATH`에 추가하세요. `GOBIN`을 설정했다면 해당 경로를,
그 외에는 `GOPATH/bin`을 사용합니다. 기본값은 `~/go/bin`이며 Windows는 `%USERPROFILE%\go\bin`입니다.

**Debian·Ubuntu:** [APT 저장소를 설정](docs/installation.md#apt-debian-and-ubuntu)한 뒤
`sudo apt-get install clivoy`로 설치합니다.
Windows는 [릴리스 파일](https://github.com/wklee610/clivoy/releases/latest)을 받으세요.
Go 1.25 이상으로 [소스에서 직접 빌드](docs/installation.md#build-from-source)할 수도 있습니다.
아래 명령은 실행 파일이 `PATH`에 있다고 가정합니다. 소스에서 빌드했다면
`clivoy` 대신 `./clivoy` 또는 `.\clivoy.exe`를 사용하세요.

**사용할 제공자만** 준비하세요.

| 제공자 | 연결 방식 | 설정 안내 |
| --- | --- | --- |
| Codex | 로컬 Codex CLI | [CLI 설정](docs/providers.md#codex-and-claude) |
| Claude | 로컬 Claude Code | [CLI 설정](docs/providers.md#codex-and-claude) |
| Gemini | 로컬 Gemini CLI | [Gemini 설정](docs/providers.md#gemini-cli) |
| Grok | 로컬 Grok Build CLI, 선택적으로 xAI API | [Grok 설정](docs/providers.md#grok-build-cli) |
| Ollama | 로컬 Ollama 서버 | [Ollama 설정](docs/providers.md#ollama) |
| vLLM, LiteLLM | OpenAI 호환 HTTP 서버 | [HTTP 설정](docs/providers.md#other-model-servers) |

Windows 사용법, CLI 경로 확인, 패키지 설치는 [설치 가이드](docs/installation.md)에 있습니다.

## Quickstart

**1. 서버를 시작합니다.**

```sh
clivoy start
```

처음 실행하면 설정 파일과 로컬 API 키를 자동으로 만들고 `0.0.0.0:9610`에서
백그라운드로 실행합니다. 내 컴퓨터에서는 `http://127.0.0.1:9610`, 다른 컴퓨터에서는
`http://<서버-IP>:9610`으로 접속하세요. 기존 파일은 덮어쓰지 않습니다.

**2. 클라이언트에서 사용할 키를 불러옵니다.**

```sh
export CLIVOY_API_KEY="$(clivoy auth)"
```

PowerShell: `$env:CLIVOY_API_KEY = clivoy auth`.
이 키는 내 Clivoy 서버에 접속하기 위한 키입니다. 제공자의 로그인이나 API 키와는 별개입니다.
`auth`는 첫 키만 출력하고, `keygen`은 생성만 합니다. [추가 키 등록 방법](docs/usage.md#what-is-clivoy_api_key).

**3. 메시지를 보냅니다.**

```sh
curl -sS http://127.0.0.1:9610/v1/chat/completions \
  -H "Authorization: Bearer $CLIVOY_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"codex","messages":[{"role":"user","content":"안녕하세요!"}]}'
```

설치한 CLI에 맞춰 `codex`를 `claude`, `gemini`, `grok`으로 바꾸면 됩니다.
[PowerShell 요청 예시](docs/installation.md#windows-powershell).

새 설정에는 네 CLI 모델 이름과 로컬 Ollama 연결이 포함됩니다. 기존 설정을 사용 중이면
[필요한 제공자 항목을 추가](docs/providers.md#existing-configurations)하세요.
새 초기화에는 [Markdown 문서 질의용 `infra` 모델](#내-문서에-질문하기)도 포함됩니다.
xAI API에 직접 연결하려면 [Grok API 설정](docs/providers.md#grok-xai-api)을 참고하세요.

CLI의 `command`는 `["codex", "--yolo"]`,
`["claude", "--dangerously-skip-permissions"]`처럼 실행 파일과 인수를 배열로
설정할 수 있습니다. 이 옵션을 직접 추가하면 승인 확인 없이 CLI 도구를 실행할 수
있으며, 기본 설정은 유지됩니다. 기존 실행 파일 문자열도 계속 지원합니다.
[CLI 옵션과 실행 권한 설정](docs/providers.md#codex-and-claude)을 참고하세요.

**Ollama를 사용한다면:** [Ollama를 설치하고 실행](docs/providers.md#ollama)한 뒤
`ollama pull llama3.2:3b`로 모델을 받고, 요청에 `model: "ollama/llama3.2:3b"`를 넣으세요.
이미 설치한 다른 모델 이름으로 바꿔도 됩니다.

소스 폴더나 릴리스 압축을 푼 폴더에서 [간단한 Python 예제](docs/usage.md#python-connectivity-test)를 `python3 scripts/test_clivoy.py`로 실행합니다.
안내에 따라 `openai`를 설치하고 파일 안의 모델 이름을 바꾸면 됩니다.

## 내 문서에 질문하기

v1.1.0부터 새로 초기화하면 빈 `~/.clivoy/knowledge/default` 폴더와 Codex를
사용하는 `infra` 모델이 만들어집니다. Windows의 폴더는
`%USERPROFILE%\.clivoy\knowledge\default`입니다. Markdown 파일을 넣고 같은
API와 키로 `model: "infra"`를 지정하면 됩니다. 관련 문서를 검색해 모델에 전달하고,
답변 끝에 출처 목록을 붙입니다. 문서 변경은 기본 30초 간격으로 반영하며,
기본적으로 관련 문서가 없으면 모델을 호출하지 않고 안내를 반환합니다.

v1.2.0에서는 RAG 모델에 `knowledge_fallback: "model"`을 설정하면 검색 결과가
없을 때 해당 모델의 백엔드가 일반 지식으로 답변합니다. 답변 앞에
“지식 베이스에서 관련 문서를 찾지 못해 일반 지식으로 답변합니다.”라는 안내를
붙이고 문서 출처 목록은 붙이지 않습니다. 기본값은 `"none"`이며 검색 오류에는
fallback을 실행하지 않습니다. 텍스트 전용 입력과 CLI 실행 제한은 그대로 적용됩니다.

기존 설정은 바뀌지 않습니다. `knowledge_bases`에 Markdown 폴더나 Obsidian
보관함을 추가하고 모델의 `knowledge`로 연결하세요. 다른 CLI나 HTTP 백엔드도
선택할 수 있습니다. RAG 모델에는 별도의 CLI 실행 설정을 적용하므로 일반 모델의
`--yolo` 같은 옵션은 전달되지 않습니다. 모든 서버 API 키는 등록된 모든 지식
베이스에 접근할 수 있습니다. [설정·검색 API·제한 안내](docs/knowledge.md) ·
[설정 예시](examples/knowledge.json).

## SDK에서 사용하기

`openai` 패키지가 설치된 Python 프로젝트에서는 다음처럼 사용합니다.

```python
import os
from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:9610/v1",
    api_key=os.environ["CLIVOY_API_KEY"],
)
response = client.chat.completions.create(
    model="gemini",
    messages=[{"role": "user", "content": "API를 한 문장으로 설명해줘."}],
)
print(response.choices[0].message.content)
```

Anthropic SDK는 같은 키와 `base_url="http://127.0.0.1:9610"`을 사용합니다.
[SDK 설치 및 실행 예시](docs/usage.md#official-sdks)에서 두 클라이언트를 확인할 수 있습니다.

## 자주 쓰는 명령어

| 할 일 | 명령어 |
| --- | --- |
| 서버 상태 확인 | `clivoy status` |
| 설치된 CLI 확인 | `clivoy doctor` |
| 사용 가능한 모델 이름 확인 | `clivoy models` |
| 설정 변경 적용 | `clivoy restart` |
| 서버 종료 | `clivoy stop` |
| 터미널에서 직접 실행 | `clivoy serve` |

기본 파일은 `~/.clivoy`에 저장됩니다. Windows에서는 `%USERPROFILE%\.clivoy`입니다.
설정은 `clivoy.json`, 키는 `auth.json`, 로그는 `logs/clivoy.log`에 있습니다.
`start`는 재부팅 후 자동 실행을 등록하지 않습니다. [전체 사용 가이드](docs/usage.md).

## 지원 범위

- OpenAI Chat Completions, 상태를 저장하지 않는 Responses, Anthropic Messages, SSE
- 모든 제공자의 텍스트 대화, Codex·Claude·HTTP 비전 모델의 이미지 입력
- 호환 HTTP 서버의 함수 도구 호출과 이미지 생성
- 같은 API로 사용하는 Markdown RAG, 문서 검색과 출처 목록
- 로컬 API 키, 요청 제한 시간, 크기가 제한된 요청 대기열

CLI 연결은 대화 내용을 하나의 프롬프트로 변환합니다. API 도구 호출과 샘플링 설정은
지원하지 않으며, Gemini와 Grok CLI는 현재 텍스트만 지원합니다. 스트리밍 응답 단위는
제공자마다 다릅니다. 이어지는 대화에서는 이전 메시지도 함께 보내세요.
각 회사 API의 일부 기능을 지원하며, 자세한 차이는 [호환성 가이드](docs/compatibility.md)에 있습니다.

로컬과 원격 요청 모두 같은 API 키를 사용합니다. 키는 신뢰할 수 있는 사람에게만 공유하세요.
CLI는 내 사용자 계정으로 실행되며 OS 수준으로 격리되지 않습니다. [보안 안내](SECURITY.md).

## 더 알아보기

- [Gemini, Grok, Ollama 연결](docs/providers.md)
- [이미지 입력과 생성](docs/images.md)
- [Markdown·Obsidian RAG](docs/knowledge.md)
- [설정 예시](examples) · [패키지 배포](docs/distribution.md)
- [기여 및 테스트 안내](CONTRIBUTING.md) · [Apache-2.0 라이선스](LICENSE)
