# Teamva · Silken

2026-11-05 用户确认采用精修版 **02「丝旋 / Silken」**。同源八瓣、45° 等角、薄荷绿 `#c2ff75` 与石墨底 `#18181b`；不再使用 TV 联字或旧节点标记。Whorl 仅保留在本机候选档案中作为备用，不作为生产界面入口。

- `icon.png`：应用与官网共用的 256px 界面标记。取自已确认的 Apple 原生图标，去掉仅用于 macOS 画布的外围留白后缩小，不修改内部几何和圆角。
- `../../desktop/build/appicon.svg`：未预裁切的 1024px 矢量母稿，同一条精确路径由八个 SVG use 引用。
- `../../desktop/build/appicon.png`：1024px macOS 图标，Apple 原生连续圆角，824px 图形居中、外围 100px 透明留白。
- `../../desktop/build/appicon.icns`：已确认的原生多尺寸图标；发布脚本在 Wails 打包后复制此文件，避免再次编码改变小尺寸效果。
- `../../desktop/build/icon_installer.icns`：与 appicon.icns 完全一致，供 DMG 卷图标使用；安装背景的标题也使用同一图形。
- `../../web/public/favicon.png` 与 `../../website/public/favicon.png`：同源 64px 浏览器图标。

界面组件直接引用本目录，不各画一份 SVG 或用文字近似。桌面、安装盘保留原生 PNG/ICNS，不通过 CSS 再次裁切。`web/src/lib/brandAssets.test.ts` 检查批准素材、共享副本、尺寸与矢量对称构造；真实页面测试另核对图片成功加载。

对称检查针对标志本体；系统方向性高光与阴影不要求中心对称。商标查重、正式签名、公证与公开发布不在本次替换范围。

## 许可与归属

本目录的原始 Silken 图形及上述同源图标适用根目录的 [Apache License 2.0](../../LICENSE)。
来源和版权归属记录在 [NOTICE](../../NOTICE)，分发时按许可证第 4 节保留适用的归属声明。
这不是强制展示或保留 Logo 图片的额外许可条件；商标使用仍受第 6 节约束，不因版权许可而自动获得商标授权。
