import { ValidationError } from "@/shared/errors/validation.error";
import type { UseFormSetError, FieldValues, Path } from "react-hook-form";

export const setFormErrors = <T extends FieldValues>(
  error: unknown,
  setError: UseFormSetError<T>
) => {
  if (error instanceof ValidationError) {
    error.errors.forEach((err) => {
      setError(err.field as Path<T>, {
        type: "server",
        message: err.message,
      });
    });
    return true;
  }
  return false;
};
