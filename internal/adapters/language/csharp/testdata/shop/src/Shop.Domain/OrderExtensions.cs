namespace Shop.Domain;

public static class OrderExtensions
{
    public static decimal Total(this Order order, decimal tax) => order[0].Total(1) + tax;
}
