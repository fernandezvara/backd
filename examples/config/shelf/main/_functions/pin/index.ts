// pin: add a pin to a team's board (chapter 20).
import { defineFunction } from "@backd/functions";

/** How many pins a team's plan allows. A team without a `plan` claim is on the free one. */
const LIMITS: Record<string, number> = { free: 5, pro: 100 };
const WRITERS = ["owner", "admin", "editor"];

export default defineFunction("main/pin", async (ctx) => {
  const { org, title, url, note } = ctx.input;
  // ctx.user.orgs is what the caller is in every team they belong to: their
  // effective roles there, the team's claims, and whether the membership
  // counts for this session (a team that requires a second factor).
  const team = ctx.user?.orgs?.[org];
  if (!team?.counts) throw ctx.error(403, "not_org_member", "You are not a member of that team");
  if (!team.roles.some((r) => WRITERS.includes(r))) {
    throw ctx.error(403, "forbidden", "Only editors, admins and owners pin");
  }

  const plan = typeof team.claims.plan === "string" ? team.claims.plan : "free";
  const limit = LIMITS[plan] ?? LIMITS.free;
  const pins = ctx.admin.db("main").collection("pins");
  const { total = 0 } = await pins.list({ where: { "_meta.org": org }, limit: 1, count: true });
  if (total >= limit) {
    throw ctx.error(409, "plan_limit", `The ${plan} plan holds ${limit} pins`);
  }

  const pin = await pins.create({ title, url, note, author: ctx.user!.id }, { org });
  return { id: pin.id, pins: total + 1, limit };
});
