---
name: pdf-fill
description: Fill PDF forms from structured data.
---

# PDF Fill

Use pdftk to fill form fields.

1. Dump fields with `pdftk form.pdf dump_data_fields`.
2. Build an FDF file.
3. Run `pdftk form.pdf fill_form data.fdf output out.pdf`.
