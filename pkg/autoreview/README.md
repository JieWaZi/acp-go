# 自动审批协议

来源：[pi-auto-approval 33de98cb](https://github.com/Europa2061/pi-auto-approval/tree/33de98cb79d8a9a23300e9826504e5b69c6e1b52)，Apache-2.0，许可证见 UPSTREAM-LICENSE。

Go 的 SystemPrompt 改编自 src/prompt.ts，Review 的 allow/deny 校验参考 src/classifier.ts；使用更严格的完整 JSON 校验，不从任意文本片段提取许可。只审查完整的待执行参数和当前用户输入；缺少证据、过大载荷、非法响应或取消都回退人工。

Python Kimi 使用当前 CLI、登录和所选模型运行无工具审查，审查目录与原生用户会话分离；Pi 在原生进程内直接复用上游 classifyAction。均不依赖上游可修改全局配置的命令入口，不把 yolo 当成自动档。审查不明时沿用标准 ACP RequestPermission；拒绝与取消仍由实际执行器门禁阻止工具调用。

安全审计元数据只包含 mode、outcome、来源、风险分类和失败类别，不包含分类器原文或凭据。
