import { connect, Client, Repository } from "fakeorm";
import { orders, users, Order } from "./schema";
const db = connect("x");
const client = new Client();
export async function listOrders() { return db.select().from(orders).where(1); }
export async function addUser(v: unknown) {
  return db.transaction(async (tx) => { await tx.insert(users).values(v); return 1; });
}
export async function renameUser() { await db.update(users).set({}).where(1); }
export async function findPosts() { return client.post.findMany(); }
export async function rawCount() { return db.execute("SELECT count(*) FROM audit_log"); }
export async function allOrders(r: Repository<Order>) { return r.find(); }
