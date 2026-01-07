/**
 * Base interface for all Use Cases (Commands and Queries).
 * Use cases are the application layer entry points that orchestrate domain logic.
 */
export interface UseCase<TRequest, TResponse> {
  execute(request: TRequest): Promise<TResponse>;
}

/**
 * Use case with no request payload (e.g., GetAllItems)
 */
export interface UseCaseWithoutRequest<TResponse> {
  execute(): Promise<TResponse>;
}
