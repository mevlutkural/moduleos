import { isAxiosError } from "axios";
import i18next from "i18next";
import { AppError } from "./app.error";
import { ApiError } from "./api.error";
import { ValidationError } from "./validation.error";
import { UnknownError } from "./unknown.error";
import type { FieldError } from "../models/field-error.model";
import type { ApiResponse } from "../types/api.types";

export class ErrorHandler {
  public static handle(error: unknown): AppError {
    if (error instanceof AppError) return error;

    if (isAxiosError(error)) {
      const response = error.response;

      if (response) {
        const data = response.data as ApiResponse<unknown>;
        const statusCode = response.status;
        const message =
          data?.message || error.message || i18next.t("common.error");

        if (
          statusCode === 400 &&
          Array.isArray(data?.content) &&
          data.content.length > 0 &&
          this.isFieldErrorArray(data.content)
        ) {
          return new ValidationError(message, data.content);
        }

        return new ApiError(message, statusCode);
      } else if (error.request) {
        return new ApiError(i18next.t("common.noResponse"), 503);
      }
    }

    if (error instanceof Error) {
      return new UnknownError(error.message, error);
    }

    return new UnknownError(i18next.t("common.unknownError"), error);
  }

  public static extractMessage(error: unknown): string {
    const appError = this.handle(error);
    return appError.message;
  }

  private static isFieldErrorArray(
    content: unknown[]
  ): content is FieldError[] {
    return content.every(
      (item) =>
        typeof item === "object" &&
        item !== null &&
        "field" in item &&
        "message" in item
    );
  }
}
