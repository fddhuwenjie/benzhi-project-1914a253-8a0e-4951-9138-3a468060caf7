package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"microclimate/internal/casecore"
	"microclimate/internal/httpapi"
	"microclimate/internal/store"
	"net/http"
	"os"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:19081", "监听地址")
	port := flag.String("PORT", "", "端口兼容参数")
	snapshot := flag.String("snapshot", ".microclimate-snapshot.json", "快照路径")
	self := flag.Bool("self-check", false, "运行自检")
	flag.Parse()
	if *port != "" {
		*addr = "127.0.0.1:" + *port
	}
	if p := os.Getenv("PORT"); p != "" && *addr == "127.0.0.1:19081" {
		*addr = "127.0.0.1:" + p
	}
	storePath := *snapshot
	if *self {
		storePath = ""
	}
	st := store.New(storePath)
	if d := st.RecoveryDiagnostic(); d != "" {
		fmt.Fprintln(os.Stderr, "快照恢复诊断："+d)
	}
	api := httpapi.New(casecore.New(st))
	if *self {
		if err := selfCheck(api.Handler()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	srv := &http.Server{Addr: *addr, Handler: api.Handler()}
	go func() { _ = srv.ListenAndServe() }()
	fmt.Println("展柜微环境异常处置台已启动 " + *addr)
	select {}
}

func selfCheck(h http.Handler) error {
	ts := &http.Server{Handler: h}
	_ = ts
	req := func(method, path string, body []byte, rev string) (int, []byte) {
		r, _ := http.NewRequest(method, "http://local"+path, bytes.NewReader(body))
		if rev != "" {
			r.Header.Set("If-Match", rev)
		}
		r.Header.Set("X-Operator", "keeper-1")
		switch {
		case bytes.Contains([]byte(path), []byte("/assign")), bytes.Contains([]byte(path), []byte("/close")):
			r.Header.Set("X-Role", "保护专员")
		case bytes.Contains([]byte(path), []byte("/inspections")):
			r.Header.Set("X-Role", "值班保管员")
		case bytes.Contains([]byte(path), []byte("/reviews")):
			r.Header.Set("X-Role", "文保专家")
		}
		w := newCapture()
		h.ServeHTTP(w, r)
		return w.code, w.buf.Bytes()
	}
	idem := fmt.Sprintf("self-check-%d", time.Now().UnixNano())
	body := fmt.Sprintf(`{"cabinet_id":"C-01","temperature":31,"humidity":72,"duration_minutes":90,"artifact_sensitivity":"高","idempotency_key":"%s"}`, idem)
	code, b := req("POST", "/v1/microclimate-events", []byte(body), "")
	if code != 201 {
		return fmt.Errorf("上报自检失败: %d", code)
	}
	var c store.MicroclimateCase
	if err := jsonUnmarshal(b, &c); err != nil {
		return err
	}
	rev := fmt.Sprint(c.Revision)
	code, _ = req("POST", "/v1/cases/"+c.ID+"/assign", []byte(`{"assignee_id":"keeper-1"}`), rev)
	if code != 200 {
		return fmt.Errorf("分派自检失败: %d", code)
	}
	rev = fmt.Sprint(c.Revision + 1)
	for n := 0; n < 3; n++ {
		receipts := make([]map[string]string, 0)
		if n == 0 {
			receipts = make([]map[string]string, 0, len(c.Checklist))
			for _, item := range c.Checklist {
				receipts = append(receipts, map[string]string{"item": item, "status": "completed", "operator": "keeper-1"})
			}
		}
		at := time.Now().UTC().Add(time.Duration(n-3) * time.Minute).Format(time.RFC3339Nano)
		payload := map[string]any{"inspector_id": "keeper-1", "temperature": 22, "humidity": 50, "duration_minutes": 90 + n, "collected_at": at, "observations": "现场复测稳定", "mitigation_actions": fmt.Sprintf("降低照明并启用除湿-%d", n), "evidence_refs": []string{"sha256:0000000000000000000000000000000000000000000000000000000000000000"}, "checklist_receipts": receipts}
		bodyBytes, _ := json.Marshal(payload)
		code, b = req("POST", "/v1/cases/"+c.ID+"/inspections", bodyBytes, rev)
		if code != 200 {
			return fmt.Errorf("检查自检失败: %d %s", code, string(b))
		}
		_ = jsonUnmarshal(b, &c)
		rev = fmt.Sprint(c.Revision)
	}
	rev = fmt.Sprint(c.Revision)
	code, b = req("POST", "/v1/cases/"+c.ID+"/reviews", []byte(`{"reviewer_id":"expert-1","decision":"通过","findings":"证据完整","rectification":"持续监测"}`), rev)
	if code != 200 {
		return fmt.Errorf("复核自检失败: %d", code)
	}
	_ = jsonUnmarshal(b, &c)
	rev = fmt.Sprint(c.Revision)
	code, _ = req("POST", "/v1/cases/"+c.ID+"/close", nil, rev)
	if code != 200 {
		return fmt.Errorf("关闭自检失败: %d", code)
	}
	fmt.Println("自检通过：异常上报、分派、检查、复核、关闭链路完整")
	return nil
}

type capture struct {
	code   int
	buf    bytes.Buffer
	header http.Header
}

func newCapture() *capture                     { return &capture{code: 200, header: make(http.Header)} }
func (c *capture) Header() http.Header         { return c.header }
func (c *capture) WriteHeader(s int)           { c.code = s }
func (c *capture) Write(b []byte) (int, error) { return c.buf.Write(b) }
func jsonUnmarshal(b []byte, v any) error      { return json.Unmarshal(b, v) }
