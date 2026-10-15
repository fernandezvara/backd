// team-plan: set a team's plan and time zone, which the pin function (chapter 20) and the
// dashboard (chapter 22) read. Both are claims of the team's organization.
import { defineFunction } from "@backd/functions";

export default defineFunction("main/team-plan", async (ctx) => {
  const { org, plan, timezone } = ctx.input;
  if (plan === undefined && timezone === undefined) {
    throw ctx.error(400, "nothing_to_set", "Give a plan, a timezone or both");
  }
  if (timezone !== undefined) {
    try {
      new Intl.DateTimeFormat("en", { timeZone: timezone });
    } catch {
      throw ctx.error(400, "invalid_timezone", "Not an IANA time zone, like Europe/Madrid");
    }
  }
  try {
    // setClaims replaces them all: start from what the team has.
    const current = await ctx.admin.orgs.get(org);
    const claims = { ...current.claims };
    if (plan !== undefined) claims.plan = plan;
    if (timezone !== undefined) claims.timezone = timezone;
    await ctx.admin.orgs.setClaims(org, claims);
    return { org, plan: claims.plan as string | undefined, timezone: claims.timezone as string | undefined };
  } catch (err) {
    // An unknown team is the caller's mistake, not a failure of ours.
    if ((err as { status?: number }).status === 404) throw ctx.error(404, "not_found", "No such team");
    throw err;
  }
});
