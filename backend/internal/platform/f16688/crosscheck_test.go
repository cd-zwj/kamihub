package f16688

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 与 16688 官方实现的交叉验证。
//
// testdata/crosscheck_node_results.json 是**用官方调试页的原始 JS 算法**
// 跑出来的结果（见 _kamihub_local/crosscheck_16688.js：它从官方 demo 页面
// 里按行号原样提取算法后 eval，不是手抄）。
//
// 这个测试的价值：证明 Go 实现与官方**逐字节一致**。
// 前面那些用例只能证明"我的实现符合我的理解"，
// 只有和官方实现对跑才能证明"我的理解是对的"。
//
// 重新生成基准值：
//   cd _kamihub_local && node crosscheck_16688.js
//   cp crosscheck_node_results.json <pkg>/testdata/

type crossCase struct {
	Name      string         `json:"name"`
	Path      string         `json:"path"`
	AppID     string         `json:"app_id"`
	Timestamp string         `json:"timestamp"`
	Nonce     string         `json:"nonce"`
	Secret    string         `json:"secret"`
	Params    map[string]any `json:"params"`
}

type crossResult struct {
	Name       string `json:"name"`
	Controller string `json:"controller"`
	Action     string `json:"action"`
	Plain      string `json:"plain"`
	Sign       string `json:"sign"`
}

func TestCrossCheckWithOfficialJS(t *testing.T) {
	casesRaw, err := os.ReadFile(filepath.Join("testdata", "crosscheck_cases.json"))
	if err != nil {
		t.Fatalf("读取用例失败: %v", err)
	}
	resultsRaw, err := os.ReadFile(filepath.Join("testdata", "crosscheck_node_results.json"))
	if err != nil {
		t.Fatalf("读取官方结果失败: %v", err)
	}

	var cases []crossCase
	if err := json.Unmarshal(casesRaw, &cases); err != nil {
		t.Fatalf("解析用例失败: %v", err)
	}
	var official []crossResult
	if err := json.Unmarshal(resultsRaw, &official); err != nil {
		t.Fatalf("解析官方结果失败: %v", err)
	}

	if len(cases) != len(official) {
		t.Fatalf("用例数不一致：cases=%d official=%d", len(cases), len(official))
	}
	if len(cases) == 0 {
		t.Fatal("用例为空，交叉验证没有意义")
	}

	for i, c := range cases {
		want := official[i]

		// 1. 从路径推导 controller/action，必须与官方 endpointOf 一致
		controller, action := EndpointOf(c.Path)
		if controller != want.Controller || action != want.Action {
			t.Errorf("[%s] EndpointOf 不一致:\n got=(%s,%s)\nwant=(%s,%s)",
				c.Name, controller, action, want.Controller, want.Action)
			continue
		}

		// 2. 组装 payload，与官方 send() 完全一致
		payload := map[string]any{
			"app_id":    c.AppID,
			"timestamp": c.Timestamp,
			"nonce":     c.Nonce,
		}
		for k, v := range c.Params {
			payload[k] = v
		}

		plain, sign, err := BuildSignature(SignMethod, controller, action, payload, c.Secret)
		if err != nil {
			t.Errorf("[%s] 计算签名出错: %v", c.Name, err)
			continue
		}

		// 3. 签名原文必须逐字节一致
		if plain != want.Plain {
			t.Errorf("[%s] 签名原文不一致:\n got=%q\nwant=%q", c.Name, plain, want.Plain)
		}
		// 4. 签名必须一致
		if sign != want.Sign {
			t.Errorf("[%s] 签名不一致:\n got=%s\nwant=%s", c.Name, sign, want.Sign)
		}
	}
}
