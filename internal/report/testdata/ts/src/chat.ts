import { web } from "fakeweb";
import { listOrders } from "./data";
const catchAsync = <T extends (...a: any[]) => Promise<unknown>>(fn: T) => (...a: Parameters<T>) => { fn(...a).catch(() => {}); };
export const getChatHandler = catchAsync(async (req: any, res: any) => { res.json(await listOrders()); });
const r = web();
r.get("/chat/:id", getChatHandler);
r.get("/chat2", catchAsync(async (req: any, res: any) => { res.json(await listOrders()); }));
