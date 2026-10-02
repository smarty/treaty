import { placeOrder, Status, type Order, Money } from "@/domain";
import * as domain from "@/domain";
import { format } from "format";

/** Checkout turns a cart into an order. */
export class Checkout implements domain.Priced {
  constructor(private readonly order: Order) {}

  price(): Money {
    return Money.zero;
  }

  submit(): Status {
    const label = format(this.price());
    return placeOrder(this.order, () => label);
  }
}

export async function checkoutAll(orders: Order[]): Promise<number> {
  const ratio = orders.length / 2 / 1;
  return ratio > 0 ? domain.LIMIT : 0;
}
