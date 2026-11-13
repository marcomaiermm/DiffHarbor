import { web, rpc, router, h } from "fakeweb";
import { listOrders, addUser, findPosts } from "./data";
import { Notifier, notifyAll } from "./notify";
const app = web();
function getOrders(_req: unknown, res: { json(x: unknown): void }) { listOrders().then((o) => res.json(o)); }
app.get("/orders", getOrders);
app.post("/users", async () => { await addUser({}); });
export const appRouter = router({
  posts: rpc.query(() => findPosts()),
  invite: rpc.mutation(({ input }) => addUser(input)),
});
export function shout(n: Notifier) { n.make().send("hi"); notifyAll(["a"]); }
export function view() { return h.createElement("div"); }
