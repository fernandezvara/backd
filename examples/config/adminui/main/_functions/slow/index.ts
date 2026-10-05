// slow: waits, so there is something to cancel.
export default async function handler() {
  await new Promise((resolve) => setTimeout(resolve, 25_000));
  return { slept: true };
}
