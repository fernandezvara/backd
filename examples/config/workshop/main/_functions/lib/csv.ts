// Turns rows into CSV text: one header line, one line per row, fields
// quoted when they contain a comma, a quote or a line break.
export function toCsv(rows: Record<string, unknown>[], columns: string[]): string {
  const field = (value: unknown): string => {
    const text = value === null || value === undefined ? "" : String(value);
    return /[",\r\n]/.test(text) ? `"${text.replaceAll('"', '""')}"` : text;
  };
  const lines = [columns.join(",")];
  for (const row of rows) lines.push(columns.map((c) => field(row[c])).join(","));
  return lines.join("\n") + "\n";
}
