const token = `LONG_TOKEN_${"0123456789abcdef".repeat(8)}`;
const link = `[${token}](https://example.test/${token})`;

export const fixtures = {
  mixed: `# GFM **报告** 与 \`header\`

普通段落与 ~~删除线~~、自动链接 https://example.test，以及 ${link} 混排。

| **步骤** | 实际 STDOUT | 退出码 | 结果 |
| --- | --- | ---: | --- |
| 分阶段输出 | STREAM_TOOL_A / STREAM_TOOL_B | 0 | 通过 |
| 长链接 | ${link} | 0 | 可达 |

\`\`\`text
${token}
SECOND_CODE_LINE
\`\`\`

- [x] 已完成
- [ ] 待检查

脚注引用[^note]。

[^note]: 脚注中的 ${link} 与 \`${token}\`。`,
  nested: `## 嵌套 **GFM** 与长内容

> 引用内的 ${link}。
>
> 3. 有序列表
>    - 第二层列表中的 \`${token}\`。
>
>      | **项目** | [链接](https://example.test) | 结果 |
>      | --- | --- | --- |
>      | 嵌套表格 | ${link} | 可达 |
>
>      \`\`\`text
>      ${token}
>      NESTED_CODE_END
>      \`\`\`

多段脚注[^detail]。

[^detail]: 脚注第一段 ${link}。

    > 脚注内引用和 \`${token}\`。

    - 脚注内列表 ${link}。`,
  normal: `## 常规报告

正常段落保留 **粗体**、*强调* 与 \`code\`。

1. 第一项
2. 第二项

> 常规引用内容。

\`\`\`text
short code
second line
\`\`\`

---

结论保持正常排版。`
};
