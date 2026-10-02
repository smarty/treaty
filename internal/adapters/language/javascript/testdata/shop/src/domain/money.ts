export default class Money {
  #cents: number;
  private readonly currency: string = "USD";
  static zero = new Money(0);

  constructor(cents: number) {
    this.#cents = cents;
  }

  times(factor: number): Money {
    return new Money(Math.round(this.#cents * factor));
  }

  get cents(): number {
    return this.#cents;
  }

  format = (locale: string): string => `${this.currency} ${this.cents / 100}`;
}
