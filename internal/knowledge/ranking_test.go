package knowledge

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestEnglishRequestPhrasingDoesNotReplaceTopicEvidence(t *testing.T) {
	root := t.TempDir()
	writeMD(t, root, "smtp.md", "# SMTP\nRelay endpoint: relay.example.test on port 2525.\n")
	writeMD(t, root, "welcome.md", "# Welcome\nTell me about SMTP is a sample question you may ask the documentation bot. Please explain SMTP, show me details about SMTP, or give me a brief overview of SMTP.\n")
	i := testIndex(t, root)
	want, err := i.Search(context.Background(), "SMTP", 5, 6000)
	if err != nil || len(want) == 0 || want[0].Path != "smtp.md" {
		t.Fatalf("bare topic did not select the guide: %v, %v", want, err)
	}
	for _, query := range []string{
		"Tell me about SMTP", "Please explain SMTP", "Can you show me details about SMTP?",
		"Give me a brief overview of SMTP", "I would like more information about SMTP",
		"Describe SMTP", "Summarize the SMTP documentation",
	} {
		hits, err := i.Search(context.Background(), query, 5, 6000)
		if err != nil || !reflect.DeepEqual(hits, want) {
			t.Errorf("request phrasing changed topic retrieval for %q: %v, %v; want %v", query, hits, err, want)
		}
	}
	for _, query := range []string{"Tell me about UnknownProduct", "Please explain UnknownProduct setup", "Tell me about", "Show me more information"} {
		hits, err := i.Search(context.Background(), query, 5, 6000)
		if err != nil || len(hits) != 0 {
			t.Errorf("generic request words established relevance for %q: %v, %v", query, hits, err)
		}
	}
}

// Synthetic documents deliberately make incidental mentions shorter and more
// repetitive than the dedicated service documents. No production text is used.
func TestServiceIdentityOutranksIncidentalBodyMentions(t *testing.T) {
	root := t.TempDir()
	writeMD(t, root, "사내_SMTP.md", "https://wiki.example.test/smtp\n# 사내 SMTP\n## 발송\nSMTP 발송 포트 2525.\n"+strings.Repeat("사전 승인을 받아 서비스를 이용합니다. 운영 시간과 담당자는 별도 안내를 확인합니다. ", 25))
	writeMD(t, root, "사내_스토리지.md", "https://wiki.example.test/storage\n# 사내 스토리지 서비스\n## 서비스 종류\n공유 파일과 객체 저장소를 제공합니다.\n"+strings.Repeat("용량을 선택한 다음 담당자에게 문의합니다. 사용량은 관리 화면에서 확인합니다. ", 55))
	writeMD(t, root, "ArgoCD_설치.md", "https://wiki.example.test/argocd\n# ArgoCD 설치\n## 준비\nArgoCD 설치 방법을 설명합니다.\n"+strings.Repeat("환경별 설정을 확인하고 필요한 서비스를 준비합니다. ", 45))
	writeMD(t, root, "메일그룹사용정책.md", "# 메일그룹 사용 정책\n이메일 주소와 메일 설정을 관리합니다. 메일그룹서비스 신규 절차입니다.")
	writeMD(t, root, "Prometheus_이메일알람.md", "# Prometheus 이메일 알람\nSMTP 설정과 포트 설정으로 메일서버알림을 구성합니다. 이메일알람을 사용합니다.")
	writeMD(t, root, "SecurityGroup.md", "# SecurityGroup 정책\n"+strings.Repeat("SMTP 포트 설정 SMTP 설정과 포트. ", 12))
	writeMD(t, root, "ExampleCloud.md", "# ExampleCloud 가이드\n"+strings.Repeat("SMTP 메일발송테스트 주소와 포트 설정 방법입니다. ", 12))
	writeMD(t, root, "Compute.md", "# Compute 소개\n"+strings.Repeat("스토리지 storage 서비스 종류 설정 구성 신청 사용. ", 10))
	writeMD(t, root, "Alertmanager.md", "# Alertmanager 설치\n"+strings.Repeat("ArgoCD 설치방법과 설치 설정으로 구성합니다. ", 12))
	writeMD(t, root, "NAS.md", "# NAS 신청\nNAS 용량 신청 절차와 전용 설정을 안내합니다.")
	for n := 0; n < 40; n++ {
		writeMD(t, root, fmt.Sprintf("other-%d.md", n), "# 일반 작업\n보조 도구 설정과 사용 안내입니다. 서로 다른 기능입니다.")
	}
	i := testIndex(t, root)
	for _, tc := range []struct{ query, path string }{
		{"SMTP", "사내_SMTP.md"},
		{"사내 SMTP 설정과 포트", "사내_SMTP.md"},
		{"사내 메일 보내는 방법", "사내_SMTP.md"},
		{"사내 스토리지 서비스 종류", "사내_스토리지.md"},
		{"스토리지", "사내_스토리지.md"},
		{"ArgoCD설치방법", "ArgoCD_설치.md"},
		{"NAS 신청", "NAS.md"},
	} {
		hits, err := i.Search(context.Background(), tc.query, 5, 12000)
		if err != nil || len(hits) == 0 || hits[0].Path != tc.path {
			var order []string
			for _, h := range hits {
				order = append(order, fmt.Sprintf("%s %.3f", h.Path, h.Score))
			}
			t.Errorf("%q: want %s first; got %v; err=%v", tc.query, tc.path, order, err)
		}
	}
}

func TestFilenameIdentityIsSearchableWithoutBodyMention(t *testing.T) {
	root := t.TempDir()
	writeMD(t, root, "사내_SMTP.md", "# 연결 정보\n발송 요청은 2525번으로 연결합니다.")
	writeMD(t, root, "general.md", "# 일반 점검\nSMTP 점검 도구를 설명합니다.")
	i := testIndex(t, root)
	hits, err := i.Search(context.Background(), "SMTP 포트", 2, 5000)
	if err != nil || len(hits) == 0 || hits[0].Path != "사내_SMTP.md" {
		t.Fatalf("filename identity missing: %v %v", hits, err)
	}
}

func TestExplicitProtocolDoesNotExpandToMailOnly(t *testing.T) {
	root := t.TempDir()
	writeMD(t, root, "mail.md", "# 메일 그룹\n메일 관리 정책입니다.")
	i := testIndex(t, root)
	hits, err := i.Search(context.Background(), "SMTP", 5, 5000)
	if err != nil || len(hits) != 0 {
		t.Fatalf("SMTP broadened to mail-only evidence: %v %v", hits, err)
	}
}

func TestExplicitTopicExcludesGenericMatches(t *testing.T) {
	root := t.TempDir()
	writeMD(t, root, "example-cluster.md", "# ExampleCluster\n클러스터 할당 절차를 설명합니다.")
	writeMD(t, root, "smtp.md", "# SMTP\n사용자 할당 절차와 클러스터 정보를 설명합니다.")
	writeMD(t, root, "monitoring.md", "# 모니터링\n클러스터 할당 절차를 설명합니다.")
	i := testIndex(t, root)
	hits, err := i.Search(context.Background(), "ExampleCluster 클러스터 할당 절차를 간단히 알려줘", 6, 12000)
	if err != nil || len(hits) != 1 || hits[0].Path != "example-cluster.md" {
		t.Fatalf("topic mixed with generic evidence: %v %v", hits, err)
	}
	hits, err = i.Search(context.Background(), "UnknownProduct 클러스터 할당", 6, 12000)
	if err != nil || len(hits) != 0 {
		t.Fatalf("unknown product fell back to generic evidence: %v %v", hits, err)
	}
}
