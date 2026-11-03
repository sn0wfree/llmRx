# llmrx-client (Python edge client)

llmRx 边缘客户端的 Python 实现，行为与 Go 版 `gateway-client` 对齐，
服务端零改动。设计文档：`docs/P18-PYTHON-CLIENT.md`。

## 安装

```bash
pip install clients/python
```

得到 `gateway-client` 命令；也可以 `python -m llmrx_client`。

## 快速开始

```bash
# 1. 在服务端管理台生成一次性接入码（15 分钟有效）
# 2. 接入（写入 client.yml，0600）
gateway-client -enroll <code> -server https://llmrx.example.com -config ~/llmrx/client.yml

# 3. 启动
gateway-client -config ~/llmrx/client.yml
```

本机入口 `http://127.0.0.1:3425/v1/chat/completions`，状态页
`http://127.0.0.1:3425/ui`，健康 JSON `/healthz`。

## 功能

- **数据面**：OpenAI 兼容转发、粘性故障转移、熔断（3 次/30s）、
  SSE 流式直通、非 chat 路径透传
- **缓存**：非流式且 temperature=0 的响应本地 LRU+TTL 缓存；
  上游不可达时用缓存兜底（`X-Llmrx-Cache: hit`）
- **用量**：proxy/cache 双源记账 → 本地 JSONL（5s）+ 批量上传（60s/批 200）
- **控制面**：30s 心跳；签名配置包 HMAC-SHA256 校验（原始字节）、
  拒绝版本降级；包只动 servers/缓存/兜底/并发上限，永不触碰令牌
- **状态页**：零外部资源，断网可用；设置表单部分更新，令牌仅显示

## 与 Go 版的差异

见 `docs/P18-PYTHON-CLIENT.md` §5。要点：修复了 Go 版"流式/透传转发
空 body"与"熔断双计数"两个缺陷；`cache_enabled`、`max_inflight` 实际
生效；`fallback_mode=local` 暂与 `cache_then_fail` 同义。

## 开发

```bash
python -m unittest discover -s clients/python/tests -v
```

测试只用标准库（含 mock 上游与 mock 服务端），无需安装任何东西。
