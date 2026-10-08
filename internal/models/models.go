package models

// User — пользователь системы.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	FullName string `json:"full_name"`
	Role     string `json:"role"`
}

// UserWithSecret — пользователь вместе с хэшем пароля.
// Используется только на сервере при входе.
type UserWithSecret struct {
	User
	PasswordHash string
}

// Course — учебный курс.
type Course struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Teacher     string `json:"teacher"`
	CreatedBy   int64  `json:"created_by"`
}

// CreateCourseInput — данные для создания курса.
type CreateCourseInput struct {
	Title       string
	Description string
	Teacher     string
}
