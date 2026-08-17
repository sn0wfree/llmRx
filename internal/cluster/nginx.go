package cluster

import (
	"log"
	"os"
	"os/exec"
	"strings"
	"text/template"
	"time"
)

// NginxUpdater Nginx 配置更新器
type NginxUpdater struct {
	// manager 节点管理器
	manager *Manager
	
	// configPath Nginx 配置文件路径
	configPath string
	
	// interval 更新间隔
	interval time.Duration
}

// NewNginxUpdater 创建 Nginx 更新器
func NewNginxUpdater(manager *Manager, configPath string) *NginxUpdater {
	return &NginxUpdater{
		manager:    manager,
		configPath: configPath,
		interval:   30 * time.Second,
	}
}

// Start 启动更新器
func (u *NginxUpdater) Start() {
	// 立即更新一次
	u.update()
	
	// 启动定时更新
	go u.updateLoop()
}

// updateLoop 更新循环
func (u *NginxUpdater) updateLoop() {
	ticker := time.NewTicker(u.interval)
	defer ticker.Stop()
	
	for range ticker.C {
		u.update()
	}
}

// update 更新 Nginx 配置
func (u *NginxUpdater) update() {
	// 获取所有在线节点
	nodes := u.manager.GetNodes()
	
	// 生成 upstream 配置
	config := u.generateUpstreamConfig(nodes)
	
	// 写入配置文件
	if err := u.writeConfig(config); err != nil {
		log.Printf("failed to write nginx config: %v", err)
		return
	}
	
	// 重新加载 Nginx
	if err := u.reloadNginx(); err != nil {
		log.Printf("failed to reload nginx: %v", err)
		return
	}
	
	log.Printf("nginx config updated with %d nodes", len(nodes))
}

// generateUpstreamConfig 生成 upstream 配置
func (u *NginxUpdater) generateUpstreamConfig(nodes []*Node) string {
	tmpl := `upstream llmrx_backend {
    least_conn;
{{range .Nodes}}    server {{.Address}}:{{.Port}} weight={{if eq .Role "primary"}}3{{else}}2{{end}};
{{end}}
    keepalive 16;
}`
	
	data := struct {
		Nodes []*Node
	}{
		Nodes: nodes,
	}
	
	var buf strings.Builder
	t, err := template.New("upstream").Parse(tmpl)
	if err != nil {
		log.Printf("failed to parse template: %v", err)
		return ""
	}
	
	if err := t.Execute(&buf, data); err != nil {
		log.Printf("failed to execute template: %v", err)
		return ""
	}
	
	return buf.String()
}

// writeConfig 写入配置文件
func (u *NginxUpdater) writeConfig(config string) error {
	return os.WriteFile(u.configPath, []byte(config), 0644)
}

// reloadNginx 重新加载 Nginx
func (u *NginxUpdater) reloadNginx() error {
	cmd := exec.Command("nginx", "-s", "reload")
	return cmd.Run()
}
