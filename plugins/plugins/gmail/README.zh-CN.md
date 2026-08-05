# Gmail 插件

[English](./README.md) | **简体中文**

通过 Google Gmail API 配合 OAuth2 读取和发送邮件。

## 功能

| 动作 | 说明 |
| ------ | ----------- |
| `read` | 拉取最近的邮件（可用 Gmail 搜索语法过滤） |
| `send` | 向收件人发送一封纯文本邮件 |

## 前置条件

- **Node.js 18+** 和 **npm**
- 一个已启用 Gmail API 的 Google Cloud 项目
- OAuth2 凭据（Client ID + Client Secret）

---

## Google Cloud 配置

### 1. 创建 Google Cloud 项目

1. 打开 [Google Cloud Console](https://console.cloud.google.com/)。
2. 点击顶部的项目下拉框，然后点 **New Project**。
3. 起个名字（如 `voiceagent-gmail`）并点击 **Create**。
4. 从下拉框中选中这个新项目。

### 2. 启用 Gmail API

1. 进入 **APIs & Services → Library**。
2. 搜索 **Gmail API**。
3. 点进去并按 **Enable**。

### 3. 配置 OAuth 同意屏幕

1. 进入 **APIs & Services → OAuth consent screen**。
2. 选择 **External**（如果你用的是 Google Workspace 且只需组织内访问，则选 **Internal**）。
3. 填写必填字段：
   - **App name** —— 例如 `VoiceAgent Gmail Plugin`
   - **User support email** —— 你的邮箱
   - **Developer contact** —— 你的邮箱
4. 点击 **Save and Continue**。
5. 在 **Scopes** 页面点击 **Add or Remove Scopes**，添加：
   - `https://www.googleapis.com/auth/gmail.readonly`
   - `https://www.googleapis.com/auth/gmail.send`
6. 一路点击 **Save and Continue** 完成剩余步骤。

> **注意：** 应用处于 "Testing" 状态时，只有你添加的测试用户才能授权。请把你自己的 Google 账号加到 **Test users** 里。

### 4. 创建 OAuth2 凭据

1. 进入 **APIs & Services → Credentials**。
2. 点击 **Create Credentials → OAuth client ID**。
3. 选择 **Web application**。
4. 把 **Authorized redirect URIs** 设为：
   ```
   http://localhost:3000/oauth2callback
   ```
5. 点击 **Create**。
6. 复制 **Client ID** 和 **Client Secret**。

---

## 插件配置

### 1. 安装依赖

```bash
cd plugins/plugins/gmail
npm install
```

### 2. 配置凭据

复制示例环境变量文件并填入你的凭据：

```bash
cp .env.example .env
```

编辑 `.env`：

```env
GMAIL_CLIENT_ID=your-client-id-here.apps.googleusercontent.com
GMAIL_CLIENT_SECRET=your-client-secret-here
GMAIL_REDIRECT_URI=http://localhost:3000/oauth2callback
```

### 3. 授权（一次性）

运行授权辅助脚本以登录并保存 token：

```bash
npx tsx authorize.ts
```

它会：
1. 打印一个 URL —— 在浏览器中打开它。
2. 用你的 Google 账号登录并授予访问权限。
3. 跳回 `localhost:3000` 并保存 `token.json`。

这一步只需要做一次。token 会自动刷新。

### 4. 启动服务端

启动（或重启）语音智能体服务端。Gmail 插件会被自动加载。

---

## 使用示例

插件跑起来之后，你可以自然地和语音智能体说话：

- *"我有新邮件吗？"*
- *"帮我看看 Alice 发来的邮件"*
- *"给 bob@example.com 发封邮件，说我会晚到 10 分钟"*
- *"读一下我的未读邮件"*

该插件在执行前需要确认（通过 `confirmation_required: true` 设置），所以智能体会在读取或发送之前先请你确认。

插件还设置了 `thinking_sound: true`，会在 Gmail API 调用进行期间通过音频流播放一段轻柔的循环提示音，让用户在等待时知道事情正在推进。

---

## 文件

| 文件 | 用途 |
| ---- | ------- |
| `plugin.yaml` | 插件清单（名称、参数、语言） |
| `index.ts` | 插件实现 |
| `authorize.ts` | 一次性 OAuth2 授权辅助脚本 |
| `package.json` | Node.js 依赖 |
| `.env.example` | 凭据模板 |
| `.env` | 你的真实凭据（已被 git 忽略） |
| `token.json` | 保存的 OAuth2 token（已被 git 忽略，由 authorize.ts 创建） |

---

## 疑难排查

| 问题 | 解决办法 |
| ------- | -------- |
| `Missing GMAIL_CLIENT_ID or GMAIL_CLIENT_SECRET` | 检查 `.env` 文件中这些值是否已填 |
| `No token.json found` | 运行 `npx tsx authorize.ts` 完成 OAuth 流程 |
| `invalid_grant` 错误 | token 已过期或被撤销。删除 `token.json` 并重新运行 `authorize.ts` |
| `Access Not Configured` | 确认 Google Cloud 项目中已启用 Gmail API |
| `Error 403: access_denied` | 在 OAuth 同意屏幕中把你的 Google 账号加为测试用户 |
