// report: pretends to build a report.
export default function handler() {
  console.log("report built");
  return { rows: 3 };
}
