---
name: csv-analysis
description: Analyse a CSV or spreadsheet with Python, checking data quality first, and report findings with a reproducible script.
---
# Spreadsheet / CSV analysis with Python

## 1. Look before computing
Write a script file (e.g. `analysis.py`) rather than one-off snippets, so
the result can be rerun. Start with:
```python
import pandas as pd
df = pd.read_csv("data.csv")  # or pd.read_excel("file.xlsx", sheet_name=None)
print(df.shape); print(df.dtypes); print(df.head(10))
print(df.isna().sum()); print(df.describe(include="all").T)
```
If pandas is missing, use the standard `csv` module or ask before
installing anything.

## 2. Check quality
- Encoding and separators (`encoding="utf-8-sig"`, `sep=";"`), header rows,
  merged cells and totals rows from spreadsheets.
- Types: numbers stored as text ("1,234", "12%"), dates in mixed formats,
  inconsistent category spellings, duplicate rows or IDs.
- Write down every cleaning step and how many rows it touched. Never drop
  rows silently.

## 3. Answer the question
- Restate the question as concrete computations (group by X, sum Y, compare
  periods). Keep raw and cleaned data separate.
- Sanity-check totals against the source (row counts, grand totals).
- Charts only when they help: save as PNG with a title, labelled axes and
  units.

## 4. Report
- Findings first, in plain language, with the key numbers.
- Caveats: data quality issues, assumptions, small samples.
- Where the script and any output files are, and how to rerun it.

Do not modify the original file; write outputs to new files.
