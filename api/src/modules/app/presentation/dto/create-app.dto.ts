import { IsNotEmpty, IsString, MaxLength, Matches } from 'class-validator';
import {
  APP_NAME_PATTERN,
  APP_NAME_VALIDATION_MESSAGE,
} from '../../domain/constants/app-name.constants';

export class CreateAppDto {
  @IsString()
  @IsNotEmpty()
  @MaxLength(100)
  @Matches(APP_NAME_PATTERN, {
    message: APP_NAME_VALIDATION_MESSAGE,
  })
  name: string;
}
