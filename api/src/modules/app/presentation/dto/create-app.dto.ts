import { IsNotEmpty, IsString, MaxLength, Matches } from 'class-validator';

export class CreateAppDto {
  @IsString()
  @IsNotEmpty()
  @MaxLength(100)
  @Matches(/^[a-z0-9][a-z0-9-]*[a-z0-9]$|^[a-z0-9]$/, {
    message:
      'Name must contain only lowercase letters, numbers, and dashes. Cannot start or end with a dash.',
  })
  name: string;
}
