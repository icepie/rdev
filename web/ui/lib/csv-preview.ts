// Adapted from BB f51e6b0 FilePreview.tsx. Row cap includes a separate header.
export interface CsvPreviewData {
  columnCount: number;
  rows: string[][];
  truncatedColumns: boolean;
  truncatedRows: boolean;
}
interface ParsedCsvRows { rows: string[][]; truncatedRows: boolean }
export const CSV_PREVIEW_MAX_COLUMNS = 100;
export const CSV_PREVIEW_MAX_ROWS = 500;
export const CSV_PREVIEW_ROW_HEIGHT_PX = 29;
export const CSV_PREVIEW_OVERSCAN_ROWS = 8;

function parseCsvRows(contents: string, maxRows: number): ParsedCsvRows {
  const rows: string[][] = [];
  let row: string[] = [];
  let field = "";
  let inQuotes = false;
  let quotedField = false;
  let endedWithLineBreak = false;

  for (let index = 0; index < contents.length; index += 1) {
    const character = contents[index];
    endedWithLineBreak = false;

    if (inQuotes) {
      if (character === '"') {
        if (contents[index + 1] === '"') {
          field += '"';
          index += 1;
        } else {
          inQuotes = false;
        }
      } else {
        field += character;
      }
      continue;
    }

    if (character === '"' && field.length === 0) {
      inQuotes = true;
      quotedField = true;
      continue;
    }

    if (character === ",") {
      row.push(field);
      field = "";
      quotedField = false;
      continue;
    }

    if (character === "\n" || character === "\r") {
      row.push(field);
      rows.push(row);
      row = [];
      field = "";
      quotedField = false;
      endedWithLineBreak = true;
      if (character === "\r" && contents[index + 1] === "\n") {
        index += 1;
      }
      if (rows.length >= maxRows) {
        return { rows, truncatedRows: index + 1 < contents.length };
      }
      continue;
    }

    field += character;
  }

  if (
    field.length > 0 ||
    row.length > 0 ||
    quotedField ||
    !endedWithLineBreak
  ) {
    row.push(field);
    rows.push(row);
  }

  return { rows, truncatedRows: false };
}

export function buildCsvPreviewData(contents: string): CsvPreviewData {
  const { rows, truncatedRows } = parseCsvRows(
    contents.replace(/^\uFEFF/, ""),
    CSV_PREVIEW_MAX_ROWS + 1,
  );
  const columnCount = rows.reduce(
    (maximum, row) => Math.max(maximum, row.length),
    0,
  );

  return {
    columnCount: Math.min(columnCount, CSV_PREVIEW_MAX_COLUMNS),
    rows,
    truncatedColumns: columnCount > CSV_PREVIEW_MAX_COLUMNS,
    truncatedRows,
  };
}

