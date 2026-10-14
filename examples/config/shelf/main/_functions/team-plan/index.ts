// team-plan: set a team's plan, which the pin function reads (chapter 20).
import { defineFunction } from "@backd/functions";

export default defineFunction("main/team-plan", async (ctx) => {
  const { org, plan } = ctx.input;
  try {
    await ctx.admin.orgs.setClaims(org, { plan });
  } catch (err) {
    // An unknown team is the caller's mistake, not a failure of ours.
    if ((err as { status?: number }).status === 404) throw ctx.error(404, "not_found", "No such team");
    throw err;
  }
  return { org, plan };
});
