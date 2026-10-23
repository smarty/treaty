namespace Shop.Domain;

public class Result<T>
{
    public Result(T value) { Value = value; }

    public T Value { get; }

    public Result<TOut> Map<TOut>(Func<T, TOut> map) => new Result<TOut>(map(Value));
}
