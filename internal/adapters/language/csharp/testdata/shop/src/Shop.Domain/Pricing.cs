namespace Shop.Domain;

public static class Pricing
{
    public static decimal Discount(decimal amount) => amount * 0.9m;
}
