# llmRx API 文档

## 一、概述

llmRx 是一个 LLM API 网关，提供 OpenAI 兼容的 API 接口。本文档描述所有可用的 API 端点。

### 1.1 基础信息

- **基础 URL**: `http://localhost:8787` (单节点) 或 `http://localhost:8080` (多节点)
- **认证方式**: Bearer Token
- **响应格式**: JSON

### 1.2 认证

所有 API 请求需要在 `Authorization` 头中携带有效的 API Key：

```
Authorization: Bearer <your-api-key>
```

## 二、LLM API 端点

### 2.1 聊天补全

**POST** `/v1/chat/completions`

创建聊天补全请求。

#### 请求

```json
{
  "model": "gpt-3.5-turbo",
  "messages": [
    {"role": "system", "content": "You are a helpful assistant."},
    {"role": "user", "content": "Hello!"}
  ],
  "temperature": 0.7,
  "max_tokens": 100,
  "stream": false
}
```

#### 响应

```json
{
  "id": "chatcmpl-123",
  "object": "chat.completion",
  "created": 1234567890,
  "model": "gpt-3.5-turbo",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "Hello! How can I help you today?"
      },
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 10,
    "completion_tokens": 20,
    "total_tokens": 30
  }
}
```

### 2.2 嵌入

**POST** `/v1/embeddings`

创建文本嵌入向量。

#### 请求

```json
{
  "model": "text-embedding-ada-002",
  "input": "The food was delicious and the waiter..."
}
```

#### 响应

```json
{
  "object": "list",
  "data": [
    {
      "object": "embedding",
      "embedding": [0.0023064255, -0.009327292, ...],
      "index": 0
    }
  ],
  "model": "text-embedding-ada-002",
  "usage": {
    "prompt_tokens": 8,
    "total_tokens": 8
  }
}
```

### 2.3 图像生成

**POST** `/v1/images/generations`

生成图像。

#### 请求

```json
{
  "model": "dall-e-3",
  "prompt": "A cute baby sea otter",
  "n": 1,
  "size": "1024x1024"
}
```

### 2.4 音频语音

**POST** `/v1/audio/speech`

将文本转换为语音。

#### 请求

```json
{
  "model": "tts-1",
  "input": "Hello world",
  "voice": "alloy"
}
```

### 2.5 音频转录

**POST** `/v1/audio/transcriptions`

将音频转换为文本。

#### 请求

```
Content-Type: multipart/form-data

file: <audio-file>
model: "whisper-1"
```

### 2.6 重排序

**POST** `/v1/rerank`

对文档进行重排序。

#### 请求

```json
{
  "model": "rerank-2",
  "query": "What is deep learning?",
  "documents": ["Deep learning is...", "Machine learning is..."]
}
```

### 2.7 模型列表

**GET** `/v1/models`

获取可用模型列表。

#### 响应

```json
{
  "object": "list",
  "data": [
    {"id": "gpt-3.5-turbo", "object": "model"},
    {"id": "gpt-4", "object": "model"}
  ]
}
```

## 三、用户 API 端点

### 3.1 用户注册

**POST** `/api/v1/register`

使用邀请码注册新用户。

#### 请求

```json
{
  "username": "user1",
  "password": "password123",
  "invite_code": "RXM-XXXXXXXX"
}
```

#### 响应

```json
{
  "user": {
    "id": 1,
    "username": "user1"
  }
}
```

### 3.2 获取用户信息

**GET** `/api/v1/me`

获取当前用户信息。

#### 响应

```json
{
  "username": "user1",
  "api_key": "sk-...",
  "plan": {
    "id": 1,
    "name": "Basic",
    "budget_usd": 50.0,
    "used_usd": 10.0
  }
}
```

### 3.3 兑换充值码

**POST** `/api/v1/me/redeem`

使用充值码增加账户余额。

#### 请求

```json
{
  "code": "RXM-XXXXXXXX"
}
```

#### 响应

```json
{
  "success": true,
  "new_budget": 60.0
}
```

## 四、管理 API 端点

### 4.1 管理员登录

**POST** `/admin/api/v1/login`

管理员登录。

#### 请求

```json
{
  "username": "admin",
  "password": "password"
}
```

### 4.2 仪表盘

**GET** `/admin/api/v1/dashboard`

获取仪表盘数据。

### 4.3 通道管理

**GET** `/admin/api/v1/channels` - 获取通道列表
**POST** `/admin/api/v1/channels` - 创建通道
**PUT** `/admin/api/v1/channels/{id}` - 更新通道
**DELETE** `/admin/api/v1/channels/{id}` - 删除通道

### 4.4 Token 管理

**GET** `/admin/api/v1/tokens` - 获取 Token 列表
**POST** `/admin/api/v1/tokens` - 创建 Token
**PUT** `/admin/api/v1/tokens/{id}` - 更新 Token
**DELETE** `/admin/api/v1/tokens/{id}` - 删除 Token

### 4.5 用户管理

**GET** `/admin/api/v1/users` - 获取用户列表
**POST** `/admin/api/v1/users/{id}/password` - 修改用户密码

### 4.6 计划管理

**GET** `/admin/api/v1/plans` - 获取计划列表
**POST** `/admin/api/v1/plans` - 创建计划
**PUT** `/admin/api/v1/plans/{id}` - 更新计划
**DELETE** `/admin/api/v1/plans/{id}` - 删除计划

### 4.7 日志查询

**GET** `/admin/api/v1/logs` - 获取日志列表

### 4.8 分析数据

**GET** `/admin/api/v1/analytics/timeseries` - 时间序列数据
**GET** `/admin/api/v1/analytics/by-model` - 按模型统计
**GET** `/admin/api/v1/analytics/by-channel` - 按通道统计

## 五、节点管理 API 端点 (仅多例模式)

### 5.1 节点注册

**POST** `/nodes/register`

注册新节点到集群。

#### 请求

```json
{
  "node_id": "llmrx-3",
  "address": "llmrx-3",
  "port": 8787,
  "role": "replica",
  "node_type": "api"
}
```

### 5.2 节点心跳

**POST** `/nodes/heartbeat`

发送心跳到主节点。

#### 请求

```json
{
  "node_id": "llmrx-3",
  "timestamp": 1234567890,
  "status": "healthy",
  "metrics": {
    "cpu_usage": 0.25,
    "memory_usage": 0.45,
    "qps": 100
  }
}
```

### 5.3 获取节点列表

**GET** `/nodes`

获取所有节点信息。

### 5.4 获取主节点信息

**GET** `/nodes/leader`

获取当前主节点信息。

### 5.5 获取节点详情

**GET** `/nodes/{id}`

获取指定节点的详细信息。

### 5.6 获取节点健康状态

**GET** `/nodes/{id}/health`

获取指定节点的健康状态。

### 5.7 节点注销

**DELETE** `/nodes/{id}`

从集群中移除指定节点。

## 六、健康检查

### 6.1 健康检查端点

**GET** `/health`

返回服务健康状态。

#### 响应

```json
{
  "status": "ok",
  "intent_backend": "disabled"
}
```

## 七、错误处理

### 7.1 错误响应格式

```json
{
  "error": {
    "message": "Error message",
    "type": "error_type",
    "code": "error_code"
  }
}
```

### 7.2 常见错误码

| 错误码 | HTTP 状态码 | 说明 |
|--------|-------------|------|
| `invalid_request` | 400 | 请求格式错误 |
| `unauthorized` | 401 | 未授权 |
| `forbidden` | 403 | 禁止访问 |
| `not_found` | 404 | 资源不存在 |
| `rate_limit_exceeded` | 429 | 请求过于频繁 |
| `internal_error` | 500 | 内部错误 |

## 八、限流

### 8.1 默认限流

- **每分钟请求**: 60 RPM
- **每分钟 Token**: 100,000 TPM

### 8.2 自定义限流

可以通过管理界面为每个用户设置自定义限流。

## 九、流式响应

### 9.1 流式请求

```json
{
  "model": "gpt-3.5-turbo",
  "messages": [{"role": "user", "content": "Hello"}],
  "stream": true
}
```

### 9.2 流式响应

```
data: {"id":"chatcmpl-123","choices":[{"delta":{"content":"Hello"},"index":0}]}

data: {"id":"chatcmpl-123","choices":[{"delta":{"content":"!"},"index":0}]}

data: [DONE]
```

## 十、示例代码

### 10.1 Python

```python
import openai

client = openai.OpenAI(
    api_key="your-api-key",
    base_url="http://localhost:8787/v1"
)

response = client.chat.completions.create(
    model="gpt-3.5-turbo",
    messages=[{"role": "user", "content": "Hello!"}]
)

print(response.choices[0].message.content)
```

### 10.2 curl

```bash
curl http://localhost:8787/v1/chat/completions \
  -H "Authorization: Bearer your-api-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

### 10.3 Node.js

```javascript
const OpenAI = require('openai');

const client = new OpenAI({
  apiKey: 'your-api-key',
  baseURL: 'http://localhost:8787/v1'
});

const response = await client.chat.completions.create({
  model: 'gpt-3.5-turbo',
  messages: [{ role: 'user', content: 'Hello!' }]
});

console.log(response.choices[0].message.content);
```
