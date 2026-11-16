import { s } from "fakeschema";
import { proc, router } from "fakeweb";
import { listItems } from "./data";

export const KINDS = ["a", "b"] as const;
export const filtersSchema = s.object({
  q: s.string().optional(),
  kinds: s
    .array(s.enum(KINDS))
    .max(KINDS.length)
    .optional(),
  ranks: s.array(s.number()).optional(),
  mode: s.enum(["x", "y"]),
});
export const listInputSchema = filtersSchema.extend({
  team: s.string(),
  cursor: s.string(),
  limit: s.number(),
});
export const itemsRouter = router({
  shop: {
    list: proc
      .input(listInputSchema)
      .query(async ({ input, ctx }) => {
        const ok = input.team !== "" && !!ctx;
        if (ok) {
          return listItems(input.team);
        }
        return [];
      }),
  },
});
