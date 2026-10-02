import Money from "./money";

/** An order a customer places. */
export interface Order<T = string> extends Priced {
  readonly id: T;
  lines?: Array<Line>;
  total(currency: string, round?: boolean): Money;
  [key: string]: unknown;
}

export interface Priced {
  price(): Money;
}

export type Line = { sku: string; quantity: number };

export enum Status {
  Open = "open",
  Closed = "closed",
}

// placeOrder records an order.
export function placeOrder<T>(order: Order<T>, notify: (id: T) => void = () => {}): Status {
  notify(order.id);
  return Status.Open;
}

export const discount = (amount: Money, rate = 0.1): Money => amount.times(1 - rate);

export const LIMIT: number = 100;

function audit(order: Order) {
  return order.id;
}

export { audit as inspect };
