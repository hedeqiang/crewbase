# 品牌与图标

## 产品标识（原创）

![Crewbase mark](crewbase-mark.svg)

**Relay（接力）**：三位成员沿上升弧线依次传递工作，下方是共同的底座（base）。透明度 50% → 75% → 100% 表示「交接进行中」，对应单一负责人与显式交接的产品理念。

| 项 | 规格 |
|---|---|
| 网格 | 32 × 32，圆角 9 |
| 底色 | Crew Indigo `#3B32C4`（不与状态色蓝 `#007CF7` 混用） |
| 图形 | 白色；圆点 r = 3.2，圆心 (9.5, 17.5)、(16, 12)、(22.5, 17.5)；底座 16 × 2.6，圆角 1.3 |
| 最小尺寸 | 16 px |
| 源文件 | `docs/brand/crewbase-mark.svg`、`apps/web/src/components/Logo.tsx`、`apps/web/index.html`（favicon） |

## Agent 头像（原创）

「队员徽章」：7 种形状（圆角方、圆、叶形、胶囊、菱形、六边形、三角）× 18 种识别色，徽章内是 Agent 名字的首字（支持中文），字色按底色亮度自动取黑或白。不使用人脸、眼睛等拟人造型，也不借用其他产品的吉祥物。状态以右下角圆点表示，识别色不与状态色混用。

## 模型厂商图标

厂商与模型来源使用 [LobeHub Icons](https://github.com/lobehub/lobe-icons)（MIT），通过 `@lobehub/icons-static-svg` 按需引入（`apps/web/src/components/ProviderMark.tsx`）。经网关（magpie、OpenRouter）的模型按模型 id 识别实际厂商；无法识别时显示中性字母标识，不自行仿制任何厂商标志或品牌色。

商标归各自所有者；图标仅用于标示模型来源。

## 原则

- 产品自身的标识、头像、插画一律原创，不复制或仿写其他产品（含 openbot、cindy 等）的 logo、吉祥物与标志性布局。
- 第三方图标只用于指代第三方本身，且遵循其许可证。
