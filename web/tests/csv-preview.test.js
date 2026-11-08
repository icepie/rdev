import { expect, it } from "vitest";
import { buildCsvPreviewData } from "../ui/lib/csv-preview";

it("reads quoted, escaped, multiline and BOM CSV without inventing a trailing row", () => {
 const preview = buildCsvPreviewData('\uFEFF"name",notes,empty\r\n"Ada, A.","line 1\r\nline ""2""",\r\nBob,,""\r\n');
 expect(preview.rows).toEqual([["name", "notes", "empty"], ["Ada, A.", 'line 1\r\nline "2"', ""], ["Bob", "", ""]]);
 expect(preview).toMatchObject({columnCount: 3, truncatedRows: false, truncatedColumns: false});
});

it("caps the preview separately from its header and reports only actual truncation", () => {
 const rows = ["name", ...Array.from({length: 500}, (_, i) => `row-${i}`)];
 const exact = buildCsvPreviewData(rows.join("\n") + "\n");
 expect(exact.rows).toHaveLength(501);
 expect(exact.truncatedRows).toBe(false);
 const overflow = buildCsvPreviewData(rows.join("\n") + "\nnot included");
 expect(overflow.rows).toHaveLength(501);
 expect(overflow.truncatedRows).toBe(true);
 const wide = buildCsvPreviewData(Array.from({length: 101}, (_, i) => `col-${i}`).join(",") + "\nshort,row");
 expect(wide.columnCount).toBe(100);
 expect(wide.truncatedColumns).toBe(true);
 expect(wide.rows[1]).toEqual(["short", "row"]);
});
