using Shop.Domain;
using Xunit;

namespace Shop.Tests;

public class OrderTests
{
    [Fact]
    public void Adds() => Assert.NotNull(new Order(1) { Customer = "test" });
}
