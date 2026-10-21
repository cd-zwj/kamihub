// 本地测试用的易支付 mock 服务器。
//
// 用途：在没有真实易支付商户的情况下，完整验证
//
//	下单 → 支付 → 回调 → 入账 的整条链路。
//
// 它按易支付的协议行为实现：
//
//	POST /submit.php   返回一段自动提交的 HTML 表单
//	POST /mapi.php     返回 JSON（含 qrcode / payurl）
//	GET  /api.php      查单，act=order
//	POST /notify       主动向商户的回调地址发起通知（可手工触发）
//
// 启动：go run ./cmd/epaymock -addr :19000 -key local_test_key -notify http://127.0.0.1:18080/api/shop/payment/notify
package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	addr       = flag.String("addr", ":19000", "监听地址")
	key        = flag.String("key", "local_test_key", "商户密钥")
	notifyURL  = flag.String("notify", "http://127.0.0.1:18080/api/shop/payment/notify", "异步通知地址")
	autoNotify = flag.Bool("auto", true, "下单后自动发起异步通知（模拟买家付款）")
)

// 已下单的订单
type order struct {
	TradeNo  string
	Money    string
	Name     string
	Paid     bool
	Upstream string
	Created  time.Time
}

var (
	mu     sync.Mutex
	orders = map[string]*order{}
)

// sign 按易支付规则签名：参数升序拼接 + 直接追加密钥 + MD5
func sign(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k == "sign" || k == "sign_type" || strings.TrimSpace(v) == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(k + "=" + params[k])
	}
	b.WriteString(*key)

	sum := md5.Sum([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func main() {
	flag.Parse()

	mux := http.NewServeMux()

	// 页面跳转支付
	mux.HandleFunc("/submit.php", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		p := formToMap(r)
		tradeNo := p["out_trade_no"]

		mu.Lock()
		orders[tradeNo] = &order{
			TradeNo: tradeNo, Money: p["money"], Name: p["name"],
			Upstream: "UP" + tradeNo, Created: time.Now(),
		}
		mu.Unlock()

		log.Printf("[mock] submit.php 下单: %s 金额=%s", tradeNo, p["money"])

		// 回一段自动提交的表单，模拟跳转到支付页
		fmt.Fprintf(w, `<html><body>
<form id="f" action="http://%s/paypage" method="post">
<input name="out_trade_no" value="%s">
</form>
<script>document.getElementById('f').submit()</script>
</body></html>`, *addr, tradeNo)

		if *autoNotify {
			go autoPay(tradeNo, p["money"])
		}
	})

	// API 支付
	mux.HandleFunc("/mapi.php", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		p := formToMap(r)

		// 校验签名（真实易支付也会校验）
		got := p["sign"]
		p["sign"] = ""
		if sign(p) != strings.ToLower(got) {
			log.Printf("[mock] mapi.php 签名错误")
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "签名错误"})
			return
		}

		tradeNo := p["out_trade_no"]
		mu.Lock()
		orders[tradeNo] = &order{
			TradeNo: tradeNo, Money: p["money"], Name: p["name"],
			Upstream: "UP" + tradeNo, Created: time.Now(),
		}
		mu.Unlock()

		log.Printf("[mock] mapi.php 下单: %s 金额=%s", tradeNo, p["money"])

		json.NewEncoder(w).Encode(map[string]any{
			"code":     1,
			"msg":      "ok",
			"trade_no": "UP" + tradeNo,
			"qrcode":   "https://mock.example.com/qr/" + tradeNo + ".png",
			"payurl":   "http://" + *addr + "/paypage?no=" + tradeNo,
		})

		if *autoNotify {
			go autoPay(tradeNo, p["money"])
		}
	})

	// 查单
	mux.HandleFunc("/api.php", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		tradeNo := q.Get("out_trade_no")

		mu.Lock()
		o, ok := orders[tradeNo]
		mu.Unlock()

		if !ok {
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "订单不存在"})
			return
		}

		status := 0
		if o.Paid {
			status = 1
		}
		json.NewEncoder(w).Encode(map[string]any{
			"code": 1, "msg": "ok",
			"trade_no": o.Upstream, "out_trade_no": o.TradeNo,
			"status": status, "money": o.Money, "name": o.Name,
		})
	})

	// 手工触发付款（测试用）
	mux.HandleFunc("/mock/pay", func(w http.ResponseWriter, r *http.Request) {
		tradeNo := r.URL.Query().Get("trade_no")
		mu.Lock()
		o, ok := orders[tradeNo]
		mu.Unlock()
		if !ok {
			http.Error(w, "订单不存在", 404)
			return
		}
		if err := doNotify(o.TradeNo, o.Money, o.Upstream); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		mu.Lock()
		o.Paid = true
		mu.Unlock()
		fmt.Fprintf(w, "已通知: %s\n", tradeNo)
	})

	// 伪造通知（测试签名校验）
	mux.HandleFunc("/mock/forged", func(w http.ResponseWriter, r *http.Request) {
		tradeNo := r.URL.Query().Get("trade_no")
		money := r.URL.Query().Get("money")

		p := map[string]string{
			"out_trade_no": tradeNo,
			"money":        money,
			"trade_status": "TRADE_SUCCESS",
			"trade_no":     "FORGED-" + tradeNo,
		}
		// 故意用错误密钥签名
		p["sign"] = signWith(p, "WRONG_KEY")
		p["sign_type"] = "MD5"

		if err := postNotify(p); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		fmt.Fprintln(w, "已发送伪造通知")
	})

	mux.HandleFunc("/paypage", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "这是模拟的易支付收银台")
	})

	log.Printf("[mock] 易支付 mock 已启动 %s，密钥=%s，通知地址=%s", *addr, *key, *notifyURL)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

// autoPay 模拟买家完成付款后上游发起通知
func autoPay(tradeNo, money string) {
	time.Sleep(1500 * time.Millisecond)
	if err := doNotify(tradeNo, money, "UP"+tradeNo); err != nil {
		log.Printf("[mock] 自动通知失败: %v", err)
		return
	}
	mu.Lock()
	if o, ok := orders[tradeNo]; ok {
		o.Paid = true
	}
	mu.Unlock()
	log.Printf("[mock] 已自动通知付款成功: %s", tradeNo)
}

func doNotify(tradeNo, money, upstream string) error {
	p := map[string]string{
		"pid":          "1001",
		"trade_no":     upstream,
		"out_trade_no": tradeNo,
		"money":        money,
		"trade_status": "TRADE_SUCCESS",
		"name":         "余额充值",
	}
	p["sign"] = signWith(p, *key)
	p["sign_type"] = "MD5"
	return postNotify(p)
}

func postNotify(p map[string]string) error {
	form := make([]string, 0, len(p))
	for k, v := range p {
		form = append(form, k+"="+v)
	}
	body := strings.Join(form, "&")

	resp, err := http.Post(*notifyURL, "application/x-www-form-urlencoded", strings.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	buf := make([]byte, 256)
	n, _ := resp.Body.Read(buf)
	text := strings.TrimSpace(string(buf[:n]))
	log.Printf("[mock] 通知响应: %q", text)
	if text != "success" {
		return fmt.Errorf("商户未返回 success，实际: %q", text)
	}
	return nil
}

func signWith(params map[string]string, k string) string {
	keys := make([]string, 0, len(params))
	for key, v := range params {
		if key == "sign" || key == "sign_type" || strings.TrimSpace(v) == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var b strings.Builder
	for i, key := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(key + "=" + params[key])
	}
	b.WriteString(k)
	sum := md5.Sum([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func formToMap(r *http.Request) map[string]string {
	out := map[string]string{}
	for k, v := range r.PostForm {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}
