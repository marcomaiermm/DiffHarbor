export class Notifier { make() { return new Mailer(); } }
export class Mailer { send(m: string) { return m; } }
export function toLabel(s: string) { return s.toUpperCase(); }
export function notifyAll(xs: string[]) { return xs.map(toLabel); }
export interface Sender { send(m: string): string }
export function relay(s: Sender) { return s.send("x"); }
export function mailTwice(m: Mailer) {
  m.send("a");
  return m.send("b");
}
