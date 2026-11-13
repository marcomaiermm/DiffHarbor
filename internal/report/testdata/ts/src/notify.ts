export class Notifier { make() { return new Mailer(); } }
export class Mailer { send(m: string) { return m; } }
export function toLabel(s: string) { return s.toUpperCase(); }
export function notifyAll(xs: string[]) { return xs.map(toLabel); }
