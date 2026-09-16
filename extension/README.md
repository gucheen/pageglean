# 拾页（PageGlean）Chromium 扩展

## 开发安装

1. 打开 `chrome://extensions`。
2. 启用“开发者模式”。
3. 选择“加载已解压的扩展程序”。
4. 选择本目录 `extension/`。
5. 在拾页网页设置中生成扩展配对码。
6. 打开扩展的“连接设置”，输入服务地址和配对码。

扩展只使用 `activeTab` 临时读取用户主动保存的页面。连接服务时，会单独请求访问用户填写的拾页服务 Origin。

修改扩展后，在 `chrome://extensions` 中点击扩展的重新加载按钮。工具栏和扩展管理页使用橙红色书页图标；保存结果通过短暂的 ✓ / ! 角标提示。

图标由 `icons/generate.py` 生成，需要 Python 和 Pillow：`python3 icons/generate.py`（在本目录运行）。
