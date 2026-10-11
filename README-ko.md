<div align="center">

[English](README.md) | [한국어](README-ko.md)

# CLIVOY: CLI + Envoy

**내 AI CLI를 하나의 간단한 API로.**

</div>

Clivoy는 **Codex, Claude, Gemini, Grok, Ollama**를 하나의 로컬 API로 사용할 수 있게 해줍니다.
기존 CLI 로그인을 그대로 사용하고, 내 Ollama 서버에 연결합니다.
**xAI API**나 다른 호환 모델 서버도 연결할 수 있습니다.
OpenAI 또는 Anthropic SDK에서 서버 주소와 모델 이름만 지정하면 됩니다.

Clivoy 실행에는 GPU나 Python이 필요 없습니다. macOS, Linux, Windows에서
실행하는 하나의 Go 프로그램입니다.

## Installation Guide

**Go 1.25 이상**을 설치한 뒤, 이 저장소의 소스 폴더에서 빌드합니다.

```sh
go build -o clivoy ./cmd/clivoy
```

Windows에서는 `go build -o clivoy.exe ./cmd/clivoy`로 빌드하고,
아래의 `./clivoy`를 `.\clivoy.exe`로 바꾸면 됩니다.
실행 파일이 `PATH`에 있으면 `clivoy`로 실행할 수 있습니다.

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
Homebrew, APT, 릴리스 다운로드, 컨테이너 설치는 각 채널에 배포된 이후 사용할 수
있습니다. 위 소스 빌드는 배포 여부와 관계없이 사용할 수 있습니다.

## Quickstart

**1. 서버를 시작합니다.**

```sh
./clivoy start
```

처음 실행하면 설정 파일과 로컬 API 키를 자동으로 만들고 `0.0.0.0:9610`에서
백그라운드로 실행합니다. 내 컴퓨터에서는 `http://127.0.0.1:9610`, 다른 컴퓨터에서는
`http://<서버-IP>:9610`으로 접속하세요. 기존 파일은 덮어쓰지 않습니다.

**2. 클라이언트에서 사용할 키를 불러옵니다.**

```sh
export CLIVOY_API_KEY="$(./clivoy auth)"
```

PowerShell: `$env:CLIVOY_API_KEY = .\clivoy.exe auth`.
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
xAI API에 직접 연결하려면 [Grok API 설정](docs/providers.md#grok-xai-api)을 참고하세요.

**Ollama를 사용한다면:** [Ollama를 설치하고 실행](docs/providers.md#ollama)한 뒤
`ollama pull llama3.2:3b`로 모델을 받고, 요청에 `model: "ollama/llama3.2:3b"`를 넣으세요.
이미 설치한 다른 모델 이름으로 바꿔도 됩니다.

[간단한 Python 예제](docs/usage.md#python-connectivity-test)는 `python3 scripts/test_clivoy.py`로 실행합니다.
안내에 따라 `openai`를 설치하고 파일 안의 모델 이름을 바꾸면 됩니다.

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
- [설정 예시](examples) · [패키지 배포](docs/distribution.md)
- [기여 및 테스트 안내](CONTRIBUTING.md) · [Apache-2.0 라이선스](LICENSE)
